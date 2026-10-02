package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/marcwoge/deckhand/internal/selfupdate"
)

// cmdSelfUpdate replaces the binary with a newer signed release.
//
// The previous binary is kept next to the new one, so going back needs no
// network access - an update that will not start is exactly when a machine has
// no spare attention for downloading things.
func cmdSelfUpdate(args []string) error {
	fs := flag.NewFlagSet("self-update", flag.ExitOnError)
	check := fs.Bool("check", false, "report whether an update exists and exit non-zero if so")
	wanted := fs.String("version", "", "install this release instead of the latest, e.g. v0.1.1")
	dest := fs.String("dest", "", "the binary to replace (default: the running one)")
	unit := fs.String("unit", "deckhand.service", "systemd unit to restart after the update")
	restart := fs.String("restart", "auto", "auto | systemctl | none")
	force := fs.Bool("force", false, "install even when the version is already current")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	updater := &selfupdate.Updater{
		Current: version,
		Dest:    *dest,
		Logf:    func(format string, args ...interface{}) { fmt.Printf("  "+format+"\n", args...) },
	}

	target := *wanted
	if target == "" {
		latest, err := updater.Latest(ctx)
		if err != nil {
			return err
		}
		target = latest
	}

	fmt.Printf("installed: %s\n", version)
	fmt.Printf("available: %s\n", target)

	if version == target && !*force {
		fmt.Println("already up to date.")
		return nil
	}
	if *check {
		fmt.Println("an update is available.")
		// Non-zero so a monitoring check can act on it.
		os.Exit(1)
	}

	result, err := updater.Update(ctx, target)
	if err != nil {
		return err
	}
	fmt.Printf("installed %s to %s\n", result.To, result.Dest)
	if result.Previous != "" {
		fmt.Printf("  previous binary kept as %s\n", result.Previous)
	}

	// The configuration has to still be valid for the new version, or the
	// service will not come back up.
	if out, err := exec.CommandContext(ctx, result.Dest, "check").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: \"deckhand check\" does not pass with this "+
			"version:\n%s\n", indentLines(strings.TrimSpace(string(out))))
		fmt.Fprintf(os.Stderr, "  roll back with: mv %s %s\n", result.Previous, result.Dest)
	} else {
		fmt.Println("  deckhand check still passes")
	}

	return restartService(ctx, *restart, *unit, result)
}

// restartService brings the worker onto the new binary, or says what to run.
//
// Deckhand needs no privileges to restart itself when it is the worker process:
// under systemd with Restart=always, launchd with KeepAlive and a Windows
// scheduled task, exiting cleanly is the restart. But this command is normally
// run by a person next to a running service, so restarting that service is what
// is wanted here.
func restartService(ctx context.Context, mode, unit string, result selfupdate.Result) error {
	switch mode {
	case "none":
		fmt.Println("not restarting (--restart none)")
		return nil
	case "auto", "systemctl":
	default:
		return fmt.Errorf("--restart takes auto, systemctl or none, got %q", mode)
	}

	if runtime.GOOS == "linux" {
		for _, systemctl := range [][]string{{"systemctl"}, {"systemctl", "--user"}} {
			if !unitIsActive(ctx, systemctl, unit) {
				continue
			}
			fmt.Printf("restarting %s\n", unit)
			cmd := exec.CommandContext(ctx, systemctl[0],
				append(append([]string{}, systemctl[1:]...), "restart", unit)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("could not restart %s: %w: %s\nroll back with: mv %s %s",
					unit, err, strings.TrimSpace(string(out)), result.Previous, result.Dest)
			}
			if !unitIsActive(ctx, systemctl, unit) {
				return fmt.Errorf("%s did not come back up.\nRoll back with: mv %s %s && %s restart %s",
					unit, result.Previous, result.Dest, strings.Join(systemctl, " "), unit)
			}
			fmt.Printf("  %s is running %s\n", unit, result.To)
			return nil
		}
		if mode == "systemctl" {
			return fmt.Errorf("%s is not an active systemd unit", unit)
		}
	}

	// Nothing was restarted, so say exactly what to run rather than leaving the
	// old binary running and the new one on disk.
	fmt.Println("the new binary is in place; restart Deckhand so it picks it up:")
	switch runtime.GOOS {
	case "darwin":
		fmt.Println("  launchctl kickstart -k gui/$UID/io.deckhand.worker")
		fmt.Println("  # or, for a system daemon: sudo launchctl kickstart -k system/io.deckhand.worker")
	case "windows":
		fmt.Println("  schtasks /End /TN Deckhand && schtasks /Run /TN Deckhand")
	default:
		fmt.Printf("  systemctl restart %s\n", unit)
	}
	return nil
}

func unitIsActive(ctx context.Context, systemctl []string, unit string) bool {
	args := append(append([]string{}, systemctl[1:]...), "is-active", "--quiet", unit)
	return exec.CommandContext(ctx, systemctl[0], args...).Run() == nil
}

func indentLines(s string) string {
	if s == "" {
		return ""
	}
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
