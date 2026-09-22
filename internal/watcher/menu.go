package watcher

// The menu turns the text commands into buttons. Typing "/rollback shop"
// correctly on a phone, at night, is how the wrong service gets rolled back, so
// every destructive action is a button plus a confirmation.
//
// Callback data has a 64-byte budget, which rules out putting a watch name in
// it. Navigation therefore carries an index into the watch list, and an action
// that changes something carries a one-time token instead: pressing a
// confirmation button spends it, so the same button found in the chat history
// tomorrow deploys nothing.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marcwoge/deckhand/internal/telegram"
)

// pendingTTL is how long a confirmation button stays live.
const pendingTTL = 2 * time.Minute

// menuCommand opens the menu.
const menuCommand = "/menu"

// pendingAction is an action waiting for its confirmation button.
type pendingAction struct {
	kind    string // "deploy" or "rollback"
	watch   string
	created time.Time
}

// menuState holds the actions a confirmation button may still trigger.
type menuState struct {
	mu      sync.Mutex
	actions map[string]pendingAction
}

// arm stores an action and returns the token that executes it.
func (m *menuState) arm(kind, watch string) (string, error) {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf[:])

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.actions == nil {
		m.actions = map[string]pendingAction{}
	}
	// Drop what has expired; nothing else ever cleans this map.
	for t, a := range m.actions {
		if time.Since(a.created) > pendingTTL {
			delete(m.actions, t)
		}
	}
	m.actions[token] = pendingAction{kind: kind, watch: watch, created: time.Now()}
	return token, nil
}

// spend consumes a token. A token works once, and only inside its TTL.
func (m *menuState) spend(token string) (pendingAction, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.actions[token]
	if !ok {
		return pendingAction{}, false
	}
	delete(m.actions, token)
	if time.Since(a.created) > pendingTTL {
		return pendingAction{}, false
	}
	return a, true
}

// view is one screen of the menu.
type view struct {
	text string
	// mono renders the text as a fixed-width block, for tables.
	mono bool
	keys telegram.Keyboard
	// notice is shown as a short toast on the button that was pressed.
	notice string
}

// backRow is the footer every subordinate screen gets.
func backRow() []telegram.Button {
	return []telegram.Button{{Text: "◀️ Menu", Data: "n:home"}}
}

// homeView is the top level.
func (e *Engine) homeView(notice string) view {
	watches := e.Watches()
	var b strings.Builder
	b.WriteString("🚢 deckhand on " + e.host + "\n")
	if e.Paused() {
		b.WriteString("⏸ paused – nothing will deploy\n")
	} else {
		b.WriteString("▶️ active\n")
	}
	b.WriteString(fmt.Sprintf("%d watch(es)", len(watches)))
	if notice != "" {
		b.WriteString("\n\n" + notice)
	}

	keys := telegram.Keyboard{
		{{Text: "📋 Status", Data: "n:status"}, {Text: "🕑 History", Data: "n:history"}},
		{{Text: "🚀 Deploy", Data: "n:deploy"}, {Text: "↩️ Rollback", Data: "n:rollback"}},
	}
	if e.Paused() {
		keys = append(keys, []telegram.Button{{Text: "▶️ Resume", Data: "a:resume"}})
	} else {
		keys = append(keys, []telegram.Button{{Text: "⏸ Pause all", Data: "a:pause"}})
	}
	keys = append(keys, []telegram.Button{{Text: "❓ Help", Data: "n:help"}})
	return view{text: b.String(), keys: keys, notice: notice}
}

// watchRows renders one button per watch, two per row.
func (e *Engine) watchRows(prefix string) []([]telegram.Button) {
	var rows [][]telegram.Button
	var row []telegram.Button
	for i, w := range e.Watches() {
		row = append(row, telegram.Button{Text: w.Name, Data: prefix + strconv.Itoa(i)})
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

// watchAt resolves an index from callback data. Indexes are stable for as long
// as the config is, and a reload can renumber them, so an out-of-range index is
// a normal outcome rather than an error.
func (e *Engine) watchAt(arg string) (string, bool) {
	i, err := strconv.Atoi(arg)
	if err != nil {
		return "", false
	}
	watches := e.Watches()
	if i < 0 || i >= len(watches) {
		return "", false
	}
	return watches[i].Name, true
}

// menuView answers one button press. It returns the screen to show.
func (e *Engine) menuView(ctx context.Context, data string) view {
	kind, arg := data, ""
	if colon := strings.IndexByte(data, ':'); colon >= 0 {
		kind, arg = data[:colon], data[colon+1:]
	}

	switch kind {
	case "n": // navigation
		return e.navView(arg)

	case "a": // an action that is safe to repeat and easy to undo
		switch arg {
		case "pause":
			reply, _ := e.runCommand(ctx, "/pause paused from the telegram menu")
			return e.homeView(reply)
		case "resume":
			reply, _ := e.runCommand(ctx, "/resume")
			return e.homeView(reply)
		}

	case "x": // a confirmed action
		action, ok := e.menu.spend(arg)
		if !ok {
			return e.homeView("⌛ That button has expired or was already used. " +
				"Open the menu again.")
		}
		reply, _ := e.runCommand(ctx, "/"+action.kind+" "+action.watch)
		return view{text: reply, keys: telegram.Keyboard{backRow()},
			notice: strings.ToUpper(action.kind[:1]) + action.kind[1:] + " started"}
	}
	return e.homeView("")
}

// navView handles everything that only shows something.
func (e *Engine) navView(arg string) view {
	name, rest := arg, ""
	if colon := strings.IndexByte(arg, ':'); colon >= 0 {
		name, rest = arg[:colon], arg[colon+1:]
	}

	switch name {
	case "home", "":
		return e.homeView("")

	case "status":
		var buf bytes.Buffer
		if err := e.PrintStatus(&buf, false); err != nil {
			return view{text: "❌ " + err.Error(), keys: telegram.Keyboard{backRow()}}
		}
		return view{text: buf.String(), mono: true, keys: telegram.Keyboard{
			{{Text: "🔄 Refresh", Data: "n:status"}}, backRow(),
		}}

	case "history":
		if rest == "" {
			return view{text: "Which watch?", keys: append(e.watchRows("n:history:"), backRow())}
		}
		watch, ok := e.watchAt(rest)
		if !ok {
			return e.homeView("That watch is gone. The config may have been reloaded.")
		}
		var buf bytes.Buffer
		if err := e.PrintHistory(&buf, watch, 10); err != nil {
			return view{text: "❌ " + err.Error(), keys: telegram.Keyboard{backRow()}}
		}
		return view{text: buf.String(), mono: true, keys: telegram.Keyboard{
			{{Text: "◀️ Watches", Data: "n:history"}}, backRow(),
		}}

	case "deploy", "rollback":
		if rest == "" {
			label := "🚀 Deploy which watch?"
			if name == "rollback" {
				label = "↩️ Roll back which watch?"
			}
			return view{text: label, keys: append(e.watchRows("n:"+name+":"), backRow())}
		}
		return e.confirmView(name, rest)

	case "help":
		return view{text: commandHelp, keys: telegram.Keyboard{backRow()}}
	}
	return e.homeView("")
}

// confirmView is the last stop before something changes on the machine.
func (e *Engine) confirmView(kind, index string) view {
	watch, ok := e.watchAt(index)
	if !ok {
		return e.homeView("That watch is gone. The config may have been reloaded.")
	}
	token, err := e.menu.arm(kind, watch)
	if err != nil {
		return e.homeView("❌ " + err.Error())
	}

	question := "🚀 Deploy " + watch + " now, ignoring its time window?"
	if kind == "rollback" {
		question = "↩️ Roll " + watch + " back to the previous revision?"
	}
	return view{
		text: question + "\n\nThis button works once, for the next " +
			pendingTTL.String() + ".",
		keys: telegram.Keyboard{
			{{Text: "✅ Yes, do it", Data: "x:" + token}},
			{{Text: "✖️ Cancel", Data: "n:" + kind}},
		},
	}
}

// handleCallback answers a button press.
func (e *Engine) handleCallback(ctx context.Context, client *telegram.Client, chatID string,
	q *telegram.CallbackQuery) {

	// The same rule as for messages: only the configured chat is served. A
	// forwarded message carries its buttons with it, so this is not academic.
	if q.Message == nil || strconv.FormatInt(q.Message.Chat.ID, 10) != chatID {
		e.logf("telegram", "ignored a button press from outside the configured chat")
		// The press is still acknowledged, otherwise the button spins forever.
		_ = client.AnswerCallback(ctx, q.ID, "Not allowed.")
		return
	}

	v := e.menuView(ctx, q.Data)
	// Telegram shows a spinner on the button until this arrives.
	if err := client.AnswerCallback(ctx, q.ID, v.notice); err != nil {
		e.logf("telegram", "could not acknowledge a button: %v", err)
	}
	if err := client.EditMessage(ctx, chatID, q.Message.MessageID, v.text, v.mono, v.keys); err != nil {
		e.logf("telegram", "could not update the menu: %v", err)
		// The menu could not be rewritten - send the answer as its own message
		// rather than leaving the press without a result.
		_ = client.SendWithKeyboard(ctx, chatID, v.text, v.mono, v.keys)
	}
}

// botCommands is the list Telegram shows behind the menu button next to the
// message field.
func botCommands() []telegram.Command {
	return []telegram.Command{
		{Command: "menu", Description: "buttons for everything below"},
		{Command: "status", Description: "what every watch is on"},
		{Command: "history", Description: "the last deployments"},
		{Command: "deploy", Description: "deploy a watch now"},
		{Command: "rollback", Description: "go back one revision"},
		{Command: "pause", Description: "hold every deployment"},
		{Command: "resume", Description: "release the hold"},
		{Command: "help", Description: "the command list"},
	}
}
