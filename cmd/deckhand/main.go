// Command deckhand watches GitHub repositories and runs a local command when
// a new release or commit shows up.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/marcwoge/deckhand/internal/config"
	"github.com/marcwoge/deckhand/internal/service"
	"github.com/marcwoge/deckhand/internal/watcher"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3"
var version = "dev"

const usage = `deckhand - watch GitHub repositories, pull new revisions, run your command.

Usage:
  deckhand run [--config FILE]              start the worker (foreground)
  deckhand check [--config FILE]            validate the configuration and exit
  deckhand doctor [--config FILE]           check connectivity, credentials and permissions
  deckhand status [--json]                  show what every watch is doing
  deckhand deploy <watch> [--force]         deploy now, ignoring the time window
  deckhand rollback <watch>                 go back to the previous revision
  deckhand history [watch] [--limit N]      show the audit log
  deckhand pause [reason] | resume [watch]  hold or release all deployments
  deckhand service install|uninstall        install as a system service
  deckhand init [--config FILE]             write a commented example config
  deckhand version

Options are documented in the README and under docs/.
`

func main() {
	log.SetFlags(0)

	// git calls the binary back as its askpass helper; the token is passed
	// through the environment so it never appears in the process list.
	if os.Getenv("DECKHAND_ASKPASS") == "1" && len(os.Args) == 2 {
		askpass(os.Args[1])
		return
	}

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "run":
		err = cmdRun(args)
	case "check":
		err = cmdCheck(args)
	case "doctor":
		err = cmdDoctor(args)
	case "status":
		err = cmdStatus(args)
	case "deploy":
		err = cmdDeploy(args)
	case "rollback":
		err = cmdRollback(args)
	case "history":
		err = cmdHistory(args)
	case "pause":
		err = cmdPause(args)
	case "resume":
		err = cmdResume(args)
	case "service":
		err = cmdService(args)
	case "init":
		err = cmdInit(args)
	case "version", "--version", "-v":
		fmt.Printf("deckhand %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "deckhand: %v\n", err)
		os.Exit(1)
	}
}

// askpass answers git's credential prompts.
func askpass(prompt string) {
	if strings.Contains(strings.ToLower(prompt), "username") {
		fmt.Println("x-access-token")
		return
	}
	fmt.Println(os.Getenv("DECKHAND_ASKPASS_TOKEN"))
}

// parseFlags parses flags that appear before, between or after positional
// arguments, so "deckhand deploy web --force" works as naturally as
// "deckhand deploy --force web".
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func addConfigFlag(fs *flag.FlagSet) *string {
	return fs.String("config", config.DefaultConfigPath(), "path to deckhand.yaml")
}

func newEngine(path string) (*watcher.Engine, *config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	eng, err := watcher.New(cfg, watcher.Options{
		Binary: exe,
		Logf: func(watch, format string, a ...interface{}) {
			log.Printf("[%s] %s", watch, fmt.Sprintf(format, a...))
		},
	})
	if err != nil {
		return nil, nil, err
	}
	return eng, cfg, nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	allowRoot := fs.Bool("allow-root", false, "run even though the process is root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if runtime.GOOS != "windows" && os.Geteuid() == 0 && !*allowRoot {
		return errors.New("refusing to run as root: create a dedicated service user " +
			"(see docs/en/security.md), or pass --allow-root if you really mean it")
	}
	eng, cfg, err := newEngine(*cfgPath)
	if err != nil {
		return err
	}
	log.Printf("deckhand %s starting, config %s, state %s", version, cfg.Path, eng.StateDir())
	if !eng.Authenticated() {
		log.Printf("warning: no GitHub token configured; only public repositories are reachable")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	watchForReload(ctx, eng)

	if err := eng.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Printf("deckhand stopped")
	return nil
}

func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	eng, cfg, err := newEngine(*cfgPath)
	if err != nil {
		return err
	}
	fmt.Printf("config %s is valid\n", cfg.Path)
	fmt.Printf("  state directory: %s\n", eng.StateDir())
	fmt.Printf("  audit log:       %s\n", eng.AuditPath())
	fmt.Printf("  github token:    %s\n", yesNo(eng.Authenticated()))
	printNotifications(cfg)
	for _, w := range eng.Watches() {
		fmt.Printf("\n  %s\n", w.Name)
		fmt.Printf("    repository: %s (auth: %s)\n", w.Repo, w.Auth)
		fmt.Printf("    trigger:    %s\n", triggerLine(w))
		fmt.Printf("    path:       %s (strategy %s)\n", w.Path, w.Strategy)
		fmt.Printf("    window:     %s\n", w.Window.Describe())
		for _, c := range w.Run {
			fmt.Printf("    run:        %s\n", c.String())
		}
		for _, warn := range warnings(w) {
			fmt.Printf("    warning:    %s\n", warn)
		}
	}
	return nil
}

// printNotifications summarises where alerts will go, because a channel that
// silently does nothing is worse than no channel at all.
func printNotifications(cfg *config.Config) {
	if len(cfg.Notify.Channels) == 0 {
		fmt.Printf("  notifications:   none configured\n")
	}
	for _, ch := range cfg.Notify.Channels {
		target := ch.URL
		if ch.Type == "telegram" {
			target = "chat " + ch.ChatID
			if ch.Commands {
				target += ", commands enabled"
			}
		}
		token, _ := ch.ResolveToken()
		auth := ""
		if token != "" {
			auth = ", authenticated"
		}
		fmt.Printf("  notifications:   %s -> %s (on: %s%s)\n", ch.Type, target,
			strings.Join(ch.Events(cfg.Notify.On), ", "), auth)
		if ch.Type == "ntfy" && token == "" && strings.Contains(ch.URL, "ntfy.sh") {
			fmt.Printf("    warning:       this ntfy.sh topic is public - anyone who guesses\n")
			fmt.Printf("                   the name can read your deployments and post to it\n")
		}
	}
	if cfg.Heartbeat.Enabled() {
		fmt.Printf("  heartbeat:       every %s to %s\n", cfg.Heartbeat.Interval, hostOf(cfg.Heartbeat.URL))
	} else {
		fmt.Printf("  heartbeat:       none - an outage of deckhand itself would go unnoticed\n")
	}
}

// hostOf keeps the secret path of a ping url out of the output.
func hostOf(raw string) string {
	rest := raw
	for _, scheme := range []string{"https://", "http://"} {
		rest = strings.TrimPrefix(rest, scheme)
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i] + "/…"
	}
	return rest
}

// warnings surfaces configurations that are legal but worth a second look.
func warnings(w *config.Watch) []string {
	var out []string
	for _, c := range w.Run {
		if c.Shell != "" {
			out = append(out, "uses \"shell:\"; prefer an argv list so repository content can never be interpreted by a shell")
		}
		if len(c.Cmd) > 0 {
			if inside(c.Cmd[0], w.Path) && !w.AllowRepoScripts {
				out = append(out, fmt.Sprintf("command %q lives inside the deployed tree: "+
					"the repository then decides what runs. Set allow_repo_scripts: true to accept that.", c.Cmd[0]))
			}
		}
	}
	if w.Trigger.Type == config.TriggerBranch && w.Verify.RequireSignedCommit == false && w.Auth == "none" {
		out = append(out, "watching a branch of a repository you do not control; consider verify.require_signed_commit")
	}
	if w.Health == nil {
		out = append(out, "no health check configured; a broken deployment will not be detected or rolled back")
	}
	return out
}

func inside(path, dir string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func triggerLine(w *config.Watch) string {
	switch w.Trigger.Type {
	case config.TriggerBranch:
		return "new commit on branch " + w.Trigger.Branch
	case config.TriggerTag:
		return "new tag matching " + w.Trigger.TagMatch
	default:
		s := "new release"
		if w.Trigger.TagMatch != "" {
			s += " matching " + w.Trigger.TagMatch
		}
		if w.Trigger.Prerelease {
			s += " (pre-releases included)"
		}
		return s
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	eng, cfg, err := newEngine(*cfgPath)
	if err != nil {
		return err
	}
	fmt.Printf("deckhand %s, config %s\n", version, cfg.Path)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	problems, err := eng.Doctor(ctx, os.Stdout)
	if err != nil {
		return err
	}
	if problems > 0 {
		// A non-zero exit makes this usable from a monitoring script.
		os.Exit(1)
	}
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	asJSON := fs.Bool("json", false, "machine readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	eng, _, err := newEngine(*cfgPath)
	if err != nil {
		return err
	}
	return eng.PrintStatus(os.Stdout, *asJSON)
}

func cmdDeploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	force := fs.Bool("force", false, "deploy even if the revision is already current")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: deckhand deploy <watch> [--force]")
	}
	eng, _, err := newEngine(*cfgPath)
	if err != nil {
		return err
	}
	w, err := eng.Watch(pos[0])
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return eng.DeployNow(ctx, w, *force)
}

func cmdRollback(args []string) error {
	fs := flag.NewFlagSet("rollback", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: deckhand rollback <watch>")
	}
	eng, _, err := newEngine(*cfgPath)
	if err != nil {
		return err
	}
	w, err := eng.Watch(pos[0])
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return eng.Rollback(ctx, w)
}

func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	limit := fs.Int("limit", 25, "number of entries to show")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	eng, _, err := newEngine(*cfgPath)
	if err != nil {
		return err
	}
	watch := ""
	if len(pos) > 0 {
		watch = pos[0]
	}
	return eng.PrintHistory(os.Stdout, watch, *limit)
}

func cmdPause(args []string) error {
	fs := flag.NewFlagSet("pause", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	eng, _, err := newEngine(*cfgPath)
	if err != nil {
		return err
	}
	reason := strings.Join(pos, " ")
	if err := eng.Pause(reason); err != nil {
		return err
	}
	fmt.Println("deployments paused; run \"deckhand resume\" to continue")
	return nil
}

func cmdResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	eng, _, err := newEngine(*cfgPath)
	if err != nil {
		return err
	}
	if len(pos) == 1 {
		w, err := eng.Watch(pos[0])
		if err != nil {
			return err
		}
		if err := eng.ResumeWatch(w); err != nil {
			return err
		}
		fmt.Printf("%s resumed\n", w.Name)
		return nil
	}
	if err := eng.Resume(); err != nil {
		return err
	}
	fmt.Println("deployments resumed")
	return nil
}

func cmdService(args []string) error {
	fs := flag.NewFlagSet("service", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	user := fs.String("user", "", "service account to run as (Linux, system-wide installs)")
	system := fs.Bool("system", false, "install for the whole machine instead of the current user")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: deckhand service install|uninstall [--system] [--user NAME]")
	}
	action := pos[0]
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	p := service.Params{Config: cfg.Path, User: *user, SystemWide: *system,
		StateDir: cfg.Defaults.StateDir}
	switch action {
	case "install":
		return service.Install(p, os.Stdout)
	case "uninstall":
		return service.Uninstall(p, os.Stdout)
	default:
		return fmt.Errorf("unknown service action %q", action)
	}
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	path := fs.String("config", "deckhand.yaml", "file to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(*path); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite it", *path)
	}
	if dir := filepath.Dir(*path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	if err := os.WriteFile(*path, []byte(exampleConfig), 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s (mode 0600)\n", *path)
	fmt.Println("edit it, then run: deckhand check")
	return nil
}
