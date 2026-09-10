package gh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVersionOrdering(t *testing.T) {
	cases := []struct{ a, b string }{
		{"v1.9.0", "v1.10.0"},
		{"v1.2.3", "v1.2.10"},
		{"1.0.0", "2.0.0"},
		{"v2.0.0", "v2.0.1"},
	}
	for _, c := range cases {
		if !versionLess(c.a, c.b) {
			t.Errorf("%s should sort below %s", c.a, c.b)
		}
		if versionLess(c.b, c.a) {
			t.Errorf("%s must not sort below %s", c.b, c.a)
		}
	}
}

// Polling must not burn rate limit: the second request carries the ETag and a
// 304 is served from the cache.
func TestConditionalRequestsUseTheCache(t *testing.T) {
	bodies := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc123"`)
		if r.Header.Get("If-None-Match") == `"abc123"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		bodies++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sha":"1111111111111111111111111111111111111111",
			"html_url":"http://example/c","commit":{"message":"first line\nbody"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	ctx := context.Background()
	first, err := c.BranchHead(ctx, "acme/app", "main")
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	second, err := c.BranchHead(ctx, "acme/app", "main")
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if first.SHA != second.SHA {
		t.Errorf("cached response differs: %s vs %s", first.SHA, second.SHA)
	}
	if first.Name != "first line" {
		t.Errorf("commit subject = %q, want the first line only", first.Name)
	}
	if bodies != 1 {
		t.Errorf("server sent %d bodies, want 1 (the rest should be 304)", bodies)
	}
}

func TestReleaseFiltering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/app/releases":
			_, _ = w.Write([]byte(`[
				{"tag_name":"v2.0.0-rc1","prerelease":true,"published_at":"2026-03-01T00:00:00Z"},
				{"tag_name":"v1.5.0","prerelease":false,"published_at":"2026-02-01T00:00:00Z"},
				{"tag_name":"nightly","prerelease":false,"published_at":"2026-04-01T00:00:00Z"},
				{"tag_name":"v1.4.0","draft":true,"published_at":"2026-05-01T00:00:00Z"}]`))
		default:
			_, _ = w.Write([]byte(`{"object":{"sha":"deadbeef","type":"commit"}}`))
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	got, err := c.LatestRelease(context.Background(), "acme/app", "v*", false)
	if err != nil {
		t.Fatalf("latest release: %v", err)
	}
	if got.Ref != "v1.5.0" {
		t.Errorf("picked %q; drafts, pre-releases and non-matching tags must be skipped", got.Ref)
	}

	got, err = c.LatestRelease(context.Background(), "acme/app", "v*", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref != "v2.0.0-rc1" {
		t.Errorf("with prerelease enabled we expect the rc, got %q", got.Ref)
	}
}
