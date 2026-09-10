// Package service installs deckhand as a background service on Linux, macOS
// and Windows.
package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Params describe the service to install.
type Params struct {
	Binary     string // absolute path to the deckhand executable
	Config     string // absolute path to deckhand.yaml
	User       string // service account (unix only); empty means the current user
	StateDir   string
	SystemWide bool // install for all users (needs root/administrator)
}

// Install writes and enables the platform's service definition.
func Install(p Params, out io.Writer) error {
	if err := p.validate(); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "linux":
		return installSystemd(p, out)
	case "darwin":
		return installLaunchd(p, out)
	case "windows":
		return installWindows(p, out)
	default:
		return fmt.Errorf("service installation is not supported on %s; run \"deckhand run\" from your own supervisor", runtime.GOOS)
	}
}

// Uninstall removes the service definition again.
func Uninstall(p Params, out io.Writer) error {
	switch runtime.GOOS {
	case "linux":
		return uninstallSystemd(p, out)
	case "darwin":
		return uninstallLaunchd(p, out)
	case "windows":
		return uninstallWindows(p, out)
	default:
		return fmt.Errorf("service management is not supported on %s", runtime.GOOS)
	}
}

func (p *Params) validate() error {
	if p.Binary == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		p.Binary = exe
	}
	abs, err := filepath.Abs(p.Binary)
	if err != nil {
		return err
	}
	p.Binary = abs
	if p.Config == "" {
		return fmt.Errorf("no config path given")
	}
	p.Config, err = filepath.Abs(p.Config)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Config); err != nil {
		return fmt.Errorf("config %s: %w", p.Config, err)
	}
	return nil
}

const systemdUnit = `[Unit]
Description=Deckhand deployment worker
Documentation=https://github.com/marcwoge/deckhand
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{BINARY}} run --config {{CONFIG}}
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=10
{{USER}}
# The worker only needs to read its config, write its state and run the
# configured commands. Everything else stays out of reach.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=full
ProtectHome=read-only
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
{{STATE}}

[Install]
WantedBy=multi-user.target
`

func installSystemd(p Params, out io.Writer) error {
	unit := strings.ReplaceAll(systemdUnit, "{{BINARY}}", p.Binary)
	unit = strings.ReplaceAll(unit, "{{CONFIG}}", p.Config)
	userLine := ""
	if p.User != "" {
		userLine = "User=" + p.User
	}
	unit = strings.ReplaceAll(unit, "{{USER}}", userLine)
	stateLine := ""
	if p.StateDir != "" {
		stateLine = "Environment=DECKHAND_STATE_DIR=" + p.StateDir
		if p.SystemWide {
			stateLine += "\nReadWritePaths=" + p.StateDir
		}
	}
	unit = strings.ReplaceAll(unit, "{{STATE}}", stateLine)

	var path string
	var systemctl []string
	if p.SystemWide {
		path = "/etc/systemd/system/deckhand.service"
		systemctl = []string{"systemctl"}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir := filepath.Join(home, ".config", "systemd", "user")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		path = filepath.Join(dir, "deckhand.service")
		systemctl = []string{"systemctl", "--user"}
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w (run with sudo for a system-wide service)", path, err)
	}
	fmt.Fprintf(out, "wrote %s\n", path)
	runQuiet(append(systemctl, "daemon-reload")...)
	enable := append(append([]string{}, systemctl...), "enable", "--now", "deckhand.service")
	if err := run(enable[0], enable[1:]...); err != nil {
		return fmt.Errorf("enabling the service: %w", err)
	}
	fmt.Fprintf(out, "service enabled and started\n  status: %s status deckhand\n  logs:   journalctl -u deckhand -f\n",
		strings.Join(systemctl, " "))
	return nil
}

func uninstallSystemd(p Params, out io.Writer) error {
	path := "/etc/systemd/system/deckhand.service"
	systemctl := []string{"systemctl"}
	if !p.SystemWide {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, ".config", "systemd", "user", "deckhand.service")
		systemctl = []string{"systemctl", "--user"}
	}
	runQuiet(append(systemctl, "disable", "--now", "deckhand.service")...)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	runQuiet(append(systemctl, "daemon-reload")...)
	fmt.Fprintf(out, "removed %s\n", path)
	return nil
}

const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.deckhand.worker</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{BINARY}}</string>
    <string>run</string>
    <string>--config</string>
    <string>{{CONFIG}}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>{{LOG}}/deckhand.log</string>
  <key>StandardErrorPath</key><string>{{LOG}}/deckhand.log</string>
</dict>
</plist>
`

func installLaunchd(p Params, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	logDir := filepath.Join(home, "Library", "Logs")
	if p.SystemWide {
		dir = "/Library/LaunchDaemons"
		logDir = "/var/log"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	plist := strings.ReplaceAll(launchdPlist, "{{BINARY}}", p.Binary)
	plist = strings.ReplaceAll(plist, "{{CONFIG}}", p.Config)
	plist = strings.ReplaceAll(plist, "{{LOG}}", logDir)
	path := filepath.Join(dir, "io.deckhand.worker.plist")
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	fmt.Fprintf(out, "wrote %s\n", path)
	runQuiet("launchctl", "unload", path)
	if err := run("launchctl", "load", "-w", path); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}
	fmt.Fprintf(out, "service loaded\n  logs: tail -f %s/deckhand.log\n", logDir)
	return nil
}

func uninstallLaunchd(p Params, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, "Library", "LaunchAgents", "io.deckhand.worker.plist")
	if p.SystemWide {
		path = "/Library/LaunchDaemons/io.deckhand.worker.plist"
	}
	runQuiet("launchctl", "unload", "-w", path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Fprintf(out, "removed %s\n", path)
	return nil
}

// installWindows registers a scheduled task that starts at boot. A scheduled
// task is used instead of a Windows service because deckhand is an ordinary
// console program; the Service Control Manager expects services to speak its
// protocol and would otherwise terminate it.
func installWindows(p Params, out io.Writer) error {
	args := []string{
		"/Create", "/F",
		"/TN", "Deckhand",
		"/TR", fmt.Sprintf(`"%s" run --config "%s"`, p.Binary, p.Config),
		"/SC", "ONSTART",
		"/RL", "LIMITED",
	}
	if p.SystemWide {
		args = append(args, "/RU", "SYSTEM")
	} else {
		args = append(args, "/RU", os.Getenv("USERNAME"), "/IT")
	}
	if err := run("schtasks", args...); err != nil {
		return fmt.Errorf("schtasks /Create: %w (run the shell as administrator)", err)
	}
	fmt.Fprintf(out, "scheduled task \"Deckhand\" created\n")
	if err := run("schtasks", "/Run", "/TN", "Deckhand"); err != nil {
		fmt.Fprintf(out, "note: could not start the task right away: %v\n", err)
	}
	fmt.Fprintf(out, "  status: schtasks /Query /TN Deckhand\n  stop:   schtasks /End /TN Deckhand\n")
	return nil
}

func uninstallWindows(_ Params, out io.Writer) error {
	if err := run("schtasks", "/Delete", "/F", "/TN", "Deckhand"); err != nil {
		return err
	}
	fmt.Fprintln(out, "scheduled task \"Deckhand\" removed")
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func runQuiet(args ...string) {
	if len(args) == 0 {
		return
	}
	cmd := exec.Command(args[0], args[1:]...)
	_ = cmd.Run()
}
