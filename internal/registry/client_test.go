package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseReference(t *testing.T) {
	cases := map[string]Reference{
		"ghcr.io/marcwoge/app:v1.2.3": {Registry: "ghcr.io", Name: "marcwoge/app", Tag: "v1.2.3", Scheme: "https"},
		"ghcr.io/marcwoge/app":        {Registry: "ghcr.io", Name: "marcwoge/app", Tag: "latest", Scheme: "https"},
		"nginx":                       {Registry: "registry-1.docker.io", Name: "library/nginx", Tag: "latest", Scheme: "https"},
		"nginx:1.27":                  {Registry: "registry-1.docker.io", Name: "library/nginx", Tag: "1.27", Scheme: "https"},
		"acme/app:2":                  {Registry: "registry-1.docker.io", Name: "acme/app", Tag: "2", Scheme: "https"},
		"registry.example.com:5000/team/app:dev": {Registry: "registry.example.com:5000",
			Name: "team/app", Tag: "dev", Scheme: "https"},
		"localhost:5000/app": {Registry: "localhost:5000", Name: "app", Tag: "latest", Scheme: "http"},
	}
	for image, want := range cases {
		got, err := ParseReference(image)
		if err != nil {
			t.Errorf("ParseReference(%q): %v", image, err)
			continue
		}
		if got != want {
			t.Errorf("ParseReference(%q) = %+v, want %+v", image, got, want)
		}
	}
}

// A reference that already pins a digest defeats the purpose: deckhand watches
// a tag so it can notice a new digest.
func TestParseReferenceRejectsPinnedAndEmpty(t *testing.T) {
	for _, image := range []string{"ghcr.io/a/b@sha256:abc", "", "ghcr.io/"} {
		if _, err := ParseReference(image); err == nil {
			t.Errorf("ParseReference(%q) should have failed", image)
		}
	}
}

// fakeRegistry answers the token challenge and serves one digest.
func fakeRegistry(t *testing.T, digest string, wantAuth string) (*httptest.Server, *int32) {
	t.Helper()
	var tokenRequests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			atomic.AddInt32(&tokenRequests, 1)
			if wantAuth != "" {
				user, pass, ok := r.BasicAuth()
				if !ok || user+":"+pass != wantAuth {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
			}
			if got := r.URL.Query().Get("scope"); !strings.HasSuffix(got, ":pull") {
				t.Errorf("scope = %q, want pull only", got)
			}
			fmt.Fprint(w, `{"token":"registry-token","expires_in":300}`)
		case strings.Contains(r.URL.Path, "/manifests/"):
			if r.Header.Get("Authorization") != "Bearer registry-token" {
				w.Header().Set("WWW-Authenticate",
					`Bearer realm="`+"http://"+r.Host+`/token",service="fake",scope="repository:acme/app:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// All four media types must be offered, or the digest could be the
			// single-arch one instead of what docker pull would use.
			offered := strings.Join(r.Header.Values("Accept"), " ")
			for _, want := range []string{"oci.image.index", "manifest.list", "oci.image.manifest", "manifest.v2"} {
				if !strings.Contains(offered, want) {
					t.Errorf("Accept does not offer %s: %q", want, offered)
				}
			}
			w.Header().Set("Docker-Content-Digest", digest)
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `{"schemaVersion":2}`)
			}
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			if r.Header.Get("Authorization") != "Bearer registry-token" {
				w.Header().Set("WWW-Authenticate",
					`Bearer realm="`+"http://"+r.Host+`/token",service="fake"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"name":"acme/app","tags":["v1.9.0","v1.10.0","latest","dev"]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &tokenRequests
}

func refFor(t *testing.T, srv *httptest.Server, tag string) Reference {
	t.Helper()
	host := strings.TrimPrefix(srv.URL, "http://")
	ref, err := ParseReference(host + "/acme/app:" + tag)
	if err != nil {
		t.Fatal(err)
	}
	ref.Scheme = "http"
	return ref
}

func TestDigestFollowsTokenChallenge(t *testing.T) {
	want := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	srv, tokenRequests := fakeRegistry(t, want, "")
	c := New(nil)

	got, err := c.Digest(context.Background(), refFor(t, srv, "latest"))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if got != want {
		t.Errorf("digest = %q, want %q", got, want)
	}

	// The token is cached, so a second read does not fetch another.
	if _, err := c.Digest(context.Background(), refFor(t, srv, "latest")); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(tokenRequests); n != 1 {
		t.Errorf("requested %d tokens, want 1 - the cache is not working", n)
	}
}

func TestDigestUsesCredentials(t *testing.T) {
	want := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	srv, _ := fakeRegistry(t, want, "marcwoge:ghp_secret")
	host := strings.TrimPrefix(srv.URL, "http://")

	// Without credentials the registry refuses.
	if _, err := New(nil).Digest(context.Background(), refFor(t, srv, "latest")); err == nil {
		t.Error("a private image without credentials must fail")
	}

	c := New(map[string]Credential{host: {Username: "marcwoge", Password: "ghp_secret"}})
	got, err := c.Digest(context.Background(), refFor(t, srv, "latest"))
	if err != nil {
		t.Fatalf("Digest with credentials: %v", err)
	}
	if got != want {
		t.Errorf("digest = %q", got)
	}
}

func TestMatchTagPrefersHigherVersion(t *testing.T) {
	srv, _ := fakeRegistry(t, "sha256:abc", "")
	c := New(nil)
	got, err := c.MatchTag(context.Background(), refFor(t, srv, "latest"), "v*")
	if err != nil {
		t.Fatalf("MatchTag: %v", err)
	}
	if got != "v1.10.0" {
		t.Errorf("MatchTag = %q, want v1.10.0 (version order, not lexical)", got)
	}
	if _, err := c.MatchTag(context.Background(), refFor(t, srv, "latest"), "release-*"); err == nil {
		t.Error("a glob matching nothing must be an error")
	}
}

func TestErrorsAreActionable(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusNotFound:        "check the image name",
		http.StatusForbidden:       "registry credentials",
		http.StatusTooManyRequests: "poll_interval",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		ref := refFor(t, srv, "latest")
		_, err := New(nil).Digest(context.Background(), ref)
		if err == nil {
			t.Errorf("HTTP %d should be an error", status)
		} else if !strings.Contains(err.Error(), want) {
			t.Errorf("HTTP %d error = %v, want it to mention %q", status, err, want)
		}
		srv.Close()
	}
}

func TestParseChallenge(t *testing.T) {
	realm, service, scope := parseChallenge(
		`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:a/b:pull"`)
	if realm != "https://ghcr.io/token" || service != "ghcr.io" || scope != "repository:a/b:pull" {
		t.Errorf("parseChallenge = %q, %q, %q", realm, service, scope)
	}
	// A scope containing a comma inside quotes must not be split.
	_, _, scope = parseChallenge(`Bearer realm="https://x/token",scope="repository:a/b:pull,push"`)
	if scope != "repository:a/b:pull,push" {
		t.Errorf("scope = %q, want the whole quoted value", scope)
	}
}

// Some registries omit the digest header on HEAD; the client must fall back.
func TestDigestFallsBackToGet(t *testing.T) {
	want := "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK) // no digest header
			return
		}
		w.Header().Set("Docker-Content-Digest", want)
		fmt.Fprint(w, `{"schemaVersion":2}`)
	}))
	defer srv.Close()

	got, err := New(nil).Digest(context.Background(), refFor(t, srv, "latest"))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if got != want {
		t.Errorf("digest = %q, want the value from the GET response", got)
	}
}
