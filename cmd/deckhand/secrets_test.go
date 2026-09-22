package main

import (
	"os"
	"path/filepath"
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
