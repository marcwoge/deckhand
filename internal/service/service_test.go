package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A user manager has no multi-user.target and a system manager has no
// default.target worth hooking into. Getting this wrong leaves the service
// installed but never started at boot.
func TestSystemdInstallTarget(t *testing.T) {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		// The unit text is still worth checking even without a live systemd.
		t.Log("no systemd here; checking the generated unit only")
	}
	for _, tc := range []struct {
		name       string
		systemWide bool
		wantTarget string
	}{
		{"user install", false, "default.target"},
		{"system install", true, "multi-user.target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unit := systemdUnit
			if tc.systemWide {
				unit = strings.ReplaceAll(unit, "{{TARGET}}", "multi-user.target")
			} else {
				unit = strings.ReplaceAll(unit, "{{TARGET}}", "default.target")
			}
			if !strings.Contains(unit, "WantedBy="+tc.wantTarget) {
				t.Errorf("unit does not want %s:\n%s", tc.wantTarget, unit)
			}
		})
	}
}

// The generated unit must carry the hardening the documentation promises.
func TestSystemdUnitIsHardened(t *testing.T) {
	for _, want := range []string{
		"NoNewPrivileges=yes", "PrivateTmp=yes", "ProtectSystem=full",
		"ProtectHome=read-only", "RestrictSUIDSGID=yes",
		"ExecReload=/bin/kill -HUP $MAINPID", "Restart=always",
	} {
		if !strings.Contains(systemdUnit, want) {
			t.Errorf("the unit is missing %q", want)
		}
	}
}

func TestParamsValidation(t *testing.T) {
	var out bytes.Buffer
	if err := Install(Params{Config: filepath.Join(t.TempDir(), "missing.yaml")}, &out); err == nil {
		t.Error("a config that does not exist must be refused")
	}
	if err := Install(Params{}, &out); err == nil {
		t.Error("an empty config path must be refused")
	}
}

func TestCurrentUserAlwaysNamesSomething(t *testing.T) {
	// The hint it feeds is useless if it renders as an empty string.
	for _, key := range []string{"USER", "LOGNAME", "USERNAME"} {
		t.Setenv(key, "")
	}
	if got := currentUser(); got == "" {
		t.Error("currentUser() must never be empty; it goes into a command the operator copies")
	}
	t.Setenv("USER", "deckhand")
	if got := currentUser(); got != "deckhand" {
		t.Errorf("currentUser() = %q, want deckhand", got)
	}
}
