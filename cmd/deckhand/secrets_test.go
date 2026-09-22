package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcwoge/deckhand/internal/config"
)

// scripts/encrypt-credentials.sh reads the names from "deckhand secrets --tsv"
// and expects them to be the names "service install" writes into the unit. If
// the two ever disagree, the script encrypts a credential under a name the
// service does not load, and the service starts without it.
func TestSecretNamesMatchTheServiceUnit(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `
version: 1
github:
  token_file: ` + tokenFile + `
registry:
  ghcr.io:
    username: me
    password_file: ` + tokenFile + `
notify:
  on: [failure]
  channels:
    - type: ntfy
      url: https://ntfy.example.com/x
      token_file: ` + tokenFile + `
watch:
  - name: shop
    repo: acme/shop
    auth:
      token_file: ` + tokenFile + `
    trigger: { type: release }
    path: ` + filepath.Join(dir, "srv") + `
    run: [["true"]]
`
	cfgPath := filepath.Join(dir, "deckhand.yaml")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	unitNames := credentialFiles(cfg)
	if len(unitNames) == 0 {
		t.Fatal("the unit hint found no credential files")
	}

	// The same set, derived the way the overview derives it.
	overview := map[string]bool{
		credentialName("github token"):     true,
		credentialName("watch shop"):       true,
		credentialName("registry ghcr.io"): true,
		credentialName("notify #1 (ntfy)"): true,
	}
	for name := range unitNames {
		if !overview[name] {
			t.Errorf("the unit loads %q but the overview never reports that name", name)
		}
	}
	for name := range overview {
		if _, ok := unitNames[name]; !ok {
			t.Errorf("the overview reports %q but the unit never loads it", name)
		}
	}
}

// The whole point of "deckhand secrets" is to say where a credential lives
// without ever saying what it is. This pins that down: every credential in the
// configuration below has a distinctive value, and none of them may appear in
// any of the three output formats.
//
// It is also the evidence behind dismissing CodeQL's go/clear-text-logging
// alerts on this file. That query treats a field named PasswordFile as holding a
// password, so the *path* of a registry credential counts as sensitive data to
// it. Renaming the field would silence the query and lose something real: the
// name is what keeps it watching the field that does hold the value.
func TestSecretsOutputNeverContainsAValue(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("VALUE-IN-A-FILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DECKHAND_TEST_SECRET_VALUE", "VALUE-IN-THE-ENVIRONMENT")

	body := `
version: 1
github:
  token_file: ` + tokenFile + `
registry:
  ghcr.io:
    username: me
    password: VALUE-INLINE-REGISTRY
notify:
  on: [failure]
  channels:
    - type: telegram
      token_env: DECKHAND_TEST_SECRET_VALUE
      chat_id: "1"
watch:
  - name: shop
    repo: acme/shop
    auth:
      token_command: ["/bin/sh", "-c", "printf VALUE-FROM-A-COMMAND"]
    trigger: { type: release }
    path: ` + filepath.Join(dir, "srv") + `
    run: [["true"]]
`
	cfgPath := filepath.Join(dir, "deckhand.yaml")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	values := []string{
		"VALUE-IN-A-FILE", "VALUE-IN-THE-ENVIRONMENT",
		"VALUE-INLINE-REGISTRY", "VALUE-FROM-A-COMMAND",
	}
	for _, format := range [][]string{nil, {"--json"}, {"--tsv"}} {
		args := append([]string{"--config", cfgPath}, format...)
		out := captureStdout(t, func() {
			if err := cmdSecrets(args); err != nil {
				t.Fatalf("%v: %v", format, err)
			}
		})
		for _, value := range values {
			if strings.Contains(out, value) {
				t.Errorf("%v output contains the credential %q:\n%s", format, value, out)
			}
		}
		if !strings.Contains(out, tokenFile) {
			t.Errorf("%v output should still name the file, so an operator can check it:\n%s",
				format, out)
		}
	}
}

// captureStdout runs f with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = file
	f()
	os.Stdout = saved
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
