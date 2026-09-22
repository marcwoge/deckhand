package watcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcwoge/deckhand/internal/config"
)

// engineFromYAML builds an engine from a config body, with %DIR% replaced by a
// temporary directory.
func engineFromYAML(t *testing.T, body string) (*Engine, error) {
	t.Helper()
	dir := t.TempDir()
	body = strings.ReplaceAll(body, "%DIR%", dir)
	path := filepath.Join(dir, "deckhand.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return New(cfg, Options{StateDir: filepath.Join(dir, "state"),
		Logf: func(string, string, ...interface{}) {}})
}

const perWatchConfig = `
version: 1
github:
  token: global-token
watch:
  - name: global
    repo: acme/one
    trigger: { type: release }
    path: %DIR%/one
    run: [["true"]]
  - name: own
    repo: other/two
    auth:
      token_env: DECKHAND_TEST_TOKEN_TWO
    trigger: { type: release }
    path: %DIR%/two
    run: [["true"]]
  - name: public
    repo: someone/three
    auth: none
    trigger: { type: release }
    path: %DIR%/three
    run: [["true"]]
`

// One token for every repository is both wrong (repositories live in different
// accounts) and a larger blast radius than necessary.
func TestPerWatchCredentials(t *testing.T) {
	t.Setenv("DECKHAND_TEST_TOKEN_TWO", "token-for-two")

	e, err := engineFromYAML(t, perWatchConfig)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ctx := context.Background()

	cases := []struct {
		watch string
		want  string
	}{
		{"global", "global-token"},
		{"own", "token-for-two"},
	}
	for _, c := range cases {
		w, err := e.Watch(c.watch)
		if err != nil {
			t.Fatal(err)
		}
		source := e.tokenFor(w)
		if source == nil {
			t.Fatalf("%s: no credential at all", c.watch)
		}
		got, err := source(ctx, w.Repo)
		if err != nil {
			t.Fatalf("%s: %v", c.watch, err)
		}
		if got != c.want {
			t.Errorf("%s uses %q, want %q", c.watch, got, c.want)
		}
	}

	pub, err := e.Watch("public")
	if err != nil {
		t.Fatal(err)
	}
	if e.tokenFor(pub) != nil {
		t.Error(`auth: none must stay anonymous`)
	}
	if e.clientFor(pub) != e.anon {
		t.Error("an anonymous watch must use the anonymous client")
	}

	own, _ := e.Watch("own")
	if e.clientFor(own) == e.client {
		t.Error("a watch with its own credential must not use the global client")
	}
	if got := e.AuthDescriptionFor(own); !strings.Contains(got, "DECKHAND_TEST_TOKEN_TWO") {
		t.Errorf("description = %q, want the source named", got)
	}
	if got := e.AuthDescriptionFor(pub); !strings.Contains(got, "none") {
		t.Errorf("description = %q, want none", got)
	}
}

// An empty per-watch credential must fail loudly. Falling back to the global
// one would silently widen the credential the operator was narrowing.
func TestEmptyPerWatchCredentialIsRefused(t *testing.T) {
	_, err := engineFromYAML(t, `
version: 1
github:
  token: global-token
watch:
  - name: own
    repo: other/two
    auth:
      token_env: DECKHAND_TEST_TOKEN_UNSET
    trigger: { type: release }
    path: %DIR%/two
    run: [["true"]]
`)
	if err == nil {
		t.Fatal("an empty credential must be refused")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %v, want it to say the credential is empty", err)
	}
}
