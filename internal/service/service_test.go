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
		// A core dump of a process holding deployment tokens writes them to disk.
		"LimitCORE=0",
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

// A credential file in the configuration should not go unmentioned: on a
// machine with a TPM it can be encrypted at rest, and the unit is where the
// operator will look.
func TestCredentialSectionOffersSystemdCreds(t *testing.T) {
	if got := credentialSection(nil); got != "" {
		t.Errorf("no credential files must produce no section, got %q", got)
	}

	got := credentialSection(map[string]string{
		"github-token":   "/etc/deckhand/github-token",
		"notify-1-token": "/etc/deckhand/telegram-token",
	})
	for _, want := range []string{
		"systemd-creds encrypt --name=github-token /etc/deckhand/github-token",
		"LoadCredentialEncrypted=github-token:/etc/deckhand/github-token.cred",
		"LoadCredentialEncrypted=notify-1-token:/etc/deckhand/notify-1-token.cred",
		"${CREDENTIALS_DIRECTORY}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the hint is missing %q:\n%s", want, got)
		}
	}
	// Every line must be a comment: an uncommented LoadCredentialEncrypted
	// would point at a file that does not exist yet and stop the service from
	// starting at all.
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "#") {
			t.Errorf("line %q is not commented out", line)
		}
	}
}

// The unit used to be written without EnvironmentFile, with the documentation
// saying to add it afterwards - easy to miss, and then the service starts with
// "no GitHub token configured" and private repositories fail quietly (#21).
func TestEnvFileLine(t *testing.T) {
	if got := envFileLine(""); got != "" {
		t.Errorf("no env file must produce no line, got %q", got)
	}
	got := envFileLine("/etc/deckhand/env")
	if got != "EnvironmentFile=/etc/deckhand/env" {
		t.Errorf("line = %q", got)
	}
	// The tolerant form would turn a missing file back into a silently
	// token-less service.
	if strings.Contains(got, "=-") {
		t.Errorf("line = %q, want a strict EnvironmentFile", got)
	}
	if !strings.Contains(systemdUnit, "{{ENVFILE}}") {
		t.Error("the unit template has no place for the env file")
	}
}

// A unit pointing at a file that is not there is the same silent failure in a
// different costume, so the installer refuses instead of writing it.
func TestEnvFileMustExist(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "deckhand.yaml")
	if err := os.WriteFile(config, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "deckhand")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := Params{Binary: binary, Config: config, EnvFile: filepath.Join(dir, "nope")}
	err := p.validate()
	if err == nil {
		t.Fatal("an env file that does not exist must be refused")
	}
	// The message has to say how to make one; being told the path is wrong is
	// not the same as being told what to do.
	if !strings.Contains(err.Error(), "install -m 600") {
		t.Errorf("error does not say how to create it: %v", err)
	}

	envFile := filepath.Join(dir, "env")
	if err := os.WriteFile(envFile, []byte("DECKHAND_GITHUB_TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.EnvFile = envFile
	if err := p.validate(); err != nil {
		t.Errorf("an env file that is there must be accepted: %v", err)
	}
}
