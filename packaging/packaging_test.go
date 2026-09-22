// Package packaging holds no code. The test here runs in the ordinary CI
// pipeline and checks the package definition, because the alternative is finding
// out that a path is wrong from a failed release - after the tag exists.
package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type nfpmConfig struct {
	Name     string `yaml:"name"`
	Arch     string `yaml:"arch"`
	Version  string `yaml:"version"`
	Contents []struct {
		Src  string `yaml:"src"`
		Dst  string `yaml:"dst"`
		Type string `yaml:"type"`
	} `yaml:"contents"`
	Scripts map[string]string `yaml:"scripts"`
}

func load(t *testing.T) nfpmConfig {
	t.Helper()
	b, err := os.ReadFile("nfpm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg nfpmConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("nfpm.yaml does not parse: %v", err)
	}
	return cfg
}

// Every file the package ships must exist. A renamed docs directory would
// otherwise turn into a failed release.
func TestPackageContentsExist(t *testing.T) {
	cfg := load(t)
	if cfg.Name != "deckhand" {
		t.Errorf("package name = %q", cfg.Name)
	}
	if len(cfg.Contents) < 5 {
		t.Fatalf("only %d entries in contents; that cannot be right", len(cfg.Contents))
	}

	for _, entry := range cfg.Contents {
		if entry.Type == "dir" {
			continue
		}
		if entry.Src == "build/deckhand" {
			// Staged by build-packages.sh from the release binary, so it only
			// exists mid-build.
			continue
		}
		if strings.Contains(entry.Src, "${") {
			t.Errorf("%s contains an environment variable; nfpm does not expand "+
				"those inside contents", entry.Src)
			continue
		}
		// Paths are relative to the repository root, which is where the release
		// workflow runs nfpm from.
		if _, err := os.Stat(filepath.Join("..", entry.Src)); err != nil {
			t.Errorf("contents entry %s -> %s: %v", entry.Src, entry.Dst, err)
		}
	}
}

// The maintainer scripts must exist and be executable, or dpkg silently skips
// them and the package installs without a service user.
func TestMaintainerScripts(t *testing.T) {
	cfg := load(t)
	for _, want := range []string{"postinstall", "preremove", "postremove"} {
		path, ok := cfg.Scripts[want]
		if !ok {
			t.Errorf("no %s script configured", want)
			continue
		}
		info, err := os.Stat(filepath.Join("..", path))
		if err != nil {
			t.Errorf("%s: %v", want, err)
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s (%s) is not executable, mode %04o", want, path, info.Mode().Perm())
		}
	}
}

// A package that quietly starts a deployment worker holding deploy credentials
// is not a package anybody wants - and without a configuration it would only
// fail in a loop. This is the one property of postinstall worth pinning down.
func TestPostinstallNeverEnablesTheService(t *testing.T) {
	b, err := os.ReadFile("scripts/postinstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	// The printed instructions tell the operator to run "systemctl enable", so
	// only what the script executes is interesting here.
	body := withoutHeredocs(string(b))
	for _, forbidden := range []string{"systemctl enable", "systemctl start", "systemctl --now"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("postinstall.sh runs %q; the service must be left to the operator", forbidden)
		}
	}
	if !strings.Contains(body, "useradd") {
		t.Error("postinstall.sh must create the service account")
	}
}

// withoutHeredocs drops the quoted heredoc bodies from a shell script, so a
// command named in a printed message is not mistaken for one being run.
func withoutHeredocs(script string) string {
	var kept []string
	delimiter := ""
	for _, line := range strings.Split(script, "\n") {
		if delimiter != "" {
			if strings.TrimSpace(line) == delimiter {
				delimiter = ""
			}
			continue
		}
		if i := strings.Index(line, "<<'"); i >= 0 {
			rest := line[i+3:]
			if end := strings.Index(rest, "'"); end > 0 {
				delimiter = rest[:end]
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// The packaged unit has to carry the same hardening the documentation promises,
// and it must not be the one thing that would make an unconfigured service loop.
func TestPackagedUnitIsHardened(t *testing.T) {
	b, err := os.ReadFile("deckhand.service")
	if err != nil {
		t.Fatal(err)
	}
	unit := string(b)
	for _, want := range []string{
		"User=deckhand", "NoNewPrivileges=yes", "ProtectSystem=full",
		"ProtectHome=read-only", "RestrictSUIDSGID=yes", "LimitCORE=0",
		"ExecReload=/bin/kill -HUP $MAINPID", "Restart=always",
		"StateDirectory=deckhand",
		// Without a configuration there is nothing to do; starting would only
		// fill the journal with the same error.
		"ConditionPathExists=/etc/deckhand/deckhand.yaml",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the packaged unit is missing %q", want)
		}
	}
	// The credentials hint must stay commented: an active LoadCredentialEncrypted
	// pointing at a file nobody has created yet stops the service from starting.
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "LoadCredentialEncrypted=") {
			t.Errorf("LoadCredentialEncrypted is active: %q", line)
		}
	}
}
