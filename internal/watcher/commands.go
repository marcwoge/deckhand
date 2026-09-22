package watcher

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/marcwoge/deckhand/internal/audit"
	"github.com/marcwoge/deckhand/internal/secret"
	"github.com/marcwoge/deckhand/internal/telegram"
)

// commandHelp is sent for /help and for anything unrecognised.
const commandHelp = `deckhand commands

/menu                buttons for all of this
/status              what every watch is on
/history [watch]     the last deployments
/deploy <watch>      deploy now, ignoring the time window
/rollback <watch>    go back to the previous revision
/pause [reason]      hold every deployment
/resume [watch]      release the hold, or clear a halted watch
/help                this text`

// maxCommandAge ignores commands that have been sitting on Telegram's servers.
// Without it, restarting deckhand could execute a "/deploy" someone sent
// yesterday.
const maxCommandAge = 5 * time.Minute

// runCommandBot polls Telegram for commands and answers them. It is only
// started when a telegram channel has commands enabled.
func (e *Engine) runCommandBot(ctx context.Context) {
	ch := e.cfg.Notify.CommandChannel()
	if ch == nil {
		return
	}
	token, err := secret.Resolve(ctx, ch.Spec())
	if err != nil {
		e.logf("telegram", "commands disabled: %v", err)
		return
	}
	client := telegram.New(token)

	username, err := client.VerifyToken(ctx)
	if err != nil {
		e.logf("telegram", "commands disabled: %v", err)
		return
	}
	e.logf("telegram", "listening for commands from chat %s as @%s", ch.ChatID, username)

	// Publishing the command list is convenience, not a precondition: a bot
	// that cannot set it still answers every command.
	if err := client.SetCommands(ctx, botCommands()); err != nil {
		e.logf("telegram", "could not publish the command list: %v", err)
	}
	e.runCommandBotWith(ctx, client, ch.ChatID)
}

// runCommandBotWith is the loop itself, separated from credential setup so it
// can be driven by a stub in tests.
func (e *Engine) runCommandBotWith(ctx context.Context, client *telegram.Client, chatID string) {
	offset := e.loadOffset()
	if offset == 0 {
		// First start: skip whatever is already queued so old messages are
		// never executed as commands.
		if updates, err := client.GetUpdates(ctx, 0, 0); err == nil && len(updates) > 0 {
			offset = updates[len(updates)-1].UpdateID + 1
			e.saveOffset(offset)
			e.logf("telegram", "skipped %d messages queued before startup", len(updates))
		}
	}

	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		updates, err := client.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			if failures == 1 || failures%10 == 0 {
				e.logf("telegram", "cannot fetch commands (%d in a row): %v", failures, err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff(30 * time.Second)):
			}
			continue
		}
		failures = 0

		for _, u := range updates {
			offset = u.UpdateID + 1
			e.handleUpdate(ctx, client, chatID, u)
		}
		e.saveOffset(offset)
	}
}

func (e *Engine) handleUpdate(ctx context.Context, client *telegram.Client, chatID string, u telegram.Update) {
	if u.CallbackQuery != nil {
		// A button press. It carries no timestamp of its own, and its one-time
		// tokens expire on their own, so the age check below does not apply.
		e.handleCallback(ctx, client, chatID, u.CallbackQuery)
		return
	}
	if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
		return
	}
	// Commands are accepted from the configured chat only. Anyone else who
	// finds the bot gets no answer at all - not even an error, which would
	// confirm that the bot exists.
	if strconv.FormatInt(u.Message.Chat.ID, 10) != chatID {
		e.logf("telegram", "ignored a message from chat %d (not the configured chat)", u.Message.Chat.ID)
		return
	}
	if age := time.Since(u.Message.Sent()); age > maxCommandAge {
		e.logf("telegram", "ignored a command that is %s old", age.Round(time.Minute))
		_ = client.Send(ctx, chatID, "⌛ Ignored: that command is "+
			age.Round(time.Minute).String()+" old. Send it again if you still want it.", false)
		return
	}

	// The menu is the only command that answers with buttons, so it is handled
	// here rather than in runCommand, which returns text.
	if isMenuCommand(u.Message.Text) {
		v := e.homeView("")
		if err := client.SendWithKeyboard(ctx, chatID, v.text, v.mono, v.keys); err != nil {
			e.logf("telegram", "could not send the menu: %v", err)
		}
		return
	}

	reply, monospace := e.runCommand(ctx, u.Message.Text)
	if reply == "" {
		return
	}
	if err := client.Send(ctx, chatID, reply, monospace); err != nil {
		e.logf("telegram", "could not reply: %v", err)
	}
}

// isMenuCommand recognises "/menu", including the "/menu@bot_name" form groups
// use.
func isMenuCommand(text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return false
	}
	cmd := strings.ToLower(fields[0])
	if at := strings.IndexByte(cmd, '@'); at >= 0 {
		cmd = cmd[:at]
	}
	// "/start" is what Telegram sends when a chat is opened for the first
	// time, and buttons are a friendlier first screen than a command list.
	return cmd == menuCommand || cmd == "menu" || cmd == "/start"
}

// runCommand executes one command and returns the reply plus whether it should
// be rendered as a fixed-width block.
func (e *Engine) runCommand(ctx context.Context, text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(fields[0])
	// Telegram appends the bot name in groups: "/status@my_bot".
	if at := strings.IndexByte(cmd, '@'); at >= 0 {
		cmd = cmd[:at]
	}
	args := fields[1:]

	audited := func(action, detail string) {
		_ = e.audit.Write(audit.Event{Watch: detail, Action: action, Result: "ok",
			Message: "via telegram"})
	}

	switch cmd {
	case "/status", "status":
		var buf bytes.Buffer
		if err := e.PrintStatus(&buf, false); err != nil {
			return "❌ " + err.Error(), false
		}
		return buf.String(), true

	case "/history", "history":
		watch := ""
		if len(args) > 0 {
			watch = args[0]
		}
		var buf bytes.Buffer
		if err := e.PrintHistory(&buf, watch, 10); err != nil {
			return "❌ " + err.Error(), false
		}
		return buf.String(), true

	case "/deploy", "deploy":
		if len(args) != 1 {
			return "Usage: /deploy <watch>", false
		}
		w, err := e.Watch(args[0])
		if err != nil {
			return "❌ " + err.Error(), false
		}
		audited("deploy-requested", w.Name)
		go func() {
			// Deployments take minutes; answer now and report the outcome
			// through the normal notification channels.
			if err := e.DeployNow(context.WithoutCancel(ctx), w, true); err != nil {
				e.logf(w.Name, "telegram deploy failed: %v", err)
			}
		}()
		return "🚀 Deploying " + w.Name + " now. You will get the result as a notification.", false

	case "/rollback", "rollback":
		if len(args) != 1 {
			return "Usage: /rollback <watch>", false
		}
		w, err := e.Watch(args[0])
		if err != nil {
			return "❌ " + err.Error(), false
		}
		audited("rollback-requested", w.Name)
		go func() {
			if err := e.Rollback(context.WithoutCancel(ctx), w); err != nil {
				e.logf(w.Name, "telegram rollback failed: %v", err)
			}
		}()
		return "↩️ Rolling back " + w.Name + ".", false

	case "/pause", "pause":
		reason := strings.Join(args, " ")
		if reason == "" {
			reason = "paused from telegram"
		}
		if err := e.Pause(reason); err != nil {
			return "❌ " + err.Error(), false
		}
		audited("pause", "")
		return "⏸ All deployments paused. Send /resume to continue.", false

	case "/resume", "resume":
		if len(args) == 1 {
			w, err := e.Watch(args[0])
			if err != nil {
				return "❌ " + err.Error(), false
			}
			if err := e.ResumeWatch(w); err != nil {
				return "❌ " + err.Error(), false
			}
			return "▶️ " + w.Name + " resumed.", false
		}
		if err := e.Resume(); err != nil {
			return "❌ " + err.Error(), false
		}
		audited("resume", "")
		return "▶️ Deployments resumed.", false

	case "/help", "help", "/start":
		return commandHelp, false

	case menuCommand, "menu":
		// Only reached when a caller bypasses handleUpdate; the buttons are
		// sent there.
		return commandHelp, false
	}

	if strings.HasPrefix(cmd, "/") {
		return "Unknown command.\n\n" + commandHelp, false
	}
	return "", false
}

// The update offset is remembered so a restart does not replay commands.
func (e *Engine) offsetPath() string { return filepath.Join(e.stateDir, "telegram_offset") }

func (e *Engine) loadOffset() int {
	data, err := os.ReadFile(e.offsetPath())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}

func (e *Engine) saveOffset(offset int) {
	if err := os.MkdirAll(e.stateDir, 0o750); err != nil {
		return
	}
	_ = os.WriteFile(e.offsetPath(), []byte(fmt.Sprintf("%d\n", offset)), 0o640)
}
