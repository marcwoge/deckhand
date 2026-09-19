package ghapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, encoded
}

// verifyAssertion checks the JWT the way GitHub would: correct structure,
// RS256 signature over header.claims, and sane timestamps.
func verifyAssertion(t *testing.T, header string, pub *rsa.PublicKey, wantIssuer string) {
	t.Helper()
	token := strings.TrimPrefix(header, "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion has %d parts, want 3", len(parts))
	}
	enc := base64.RawURLEncoding
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature is not base64url: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	var claims struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	raw, _ := enc.DecodeString(parts[1])
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("claims are not JSON: %v", err)
	}
	if claims.Iss != wantIssuer {
		t.Errorf("iss = %q, want %q", claims.Iss, wantIssuer)
	}
	now := time.Now().Unix()
	if claims.Iat > now {
		t.Errorf("iat is in the future, which GitHub rejects")
	}
	// GitHub refuses anything longer than ten minutes.
	if claims.Exp-claims.Iat > 600 {
		t.Errorf("assertion lives %ds, GitHub allows at most 600", claims.Exp-claims.Iat)
	}
	if claims.Exp <= now {
		t.Errorf("assertion is already expired")
	}
}

func TestMintsTokenWithAVerifiableAssertion(t *testing.T) {
	key, pemKey := testKey(t)
	var minted int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyAssertion(t, r.Header.Get("Authorization"), &key.PublicKey, "123456")
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			if !strings.Contains(r.URL.Path, "/app/installations/42/") {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			atomic.AddInt32(&minted, 1)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"ghs_secret","expires_at":%q}`,
				time.Now().Add(time.Hour).Format(time.RFC3339))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a, err := New(srv.URL, "123456", pemKey, 42)
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.TokenFor(context.Background(), "acme/app")
	if err != nil {
		t.Fatalf("TokenFor: %v", err)
	}
	if token != "ghs_secret" {
		t.Errorf("token = %q", token)
	}

	// A second call must reuse the cached token rather than mint another.
	if _, err := a.TokenFor(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&minted); got != 1 {
		t.Errorf("minted %d tokens, want 1 - the cache is not being used", got)
	}
}

// A token about to expire must be replaced before a deployment starts with it.
func TestRenewsTokenNearExpiry(t *testing.T) {
	_, pemKey := testKey(t)
	var minted int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&minted, 1)
		// The first token is nearly expired, the second is fresh.
		exp := time.Now().Add(2 * time.Minute)
		if n > 1 {
			exp = time.Now().Add(time.Hour)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_%d","expires_at":%q}`, n, exp.Format(time.RFC3339))
	}))
	defer srv.Close()

	a, err := New(srv.URL, "1", pemKey, 7)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := a.TokenFor(context.Background(), "acme/app")
	second, _ := a.TokenFor(context.Background(), "acme/app")
	if first == second {
		t.Error("a token inside the renewal window must be replaced")
	}
	if second != "ghs_2" {
		t.Errorf("second token = %q, want the renewed one", second)
	}
}

// Losing the API briefly must not fail a deployment while a usable token is
// still in hand.
func TestKeepsUsableTokenWhenRenewalFails(t *testing.T) {
	_, pemKey := testKey(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"ghs_good","expires_at":%q}`,
				time.Now().Add(2*time.Minute).Format(time.RFC3339))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	a, _ := New(srv.URL, "1", pemKey, 7)
	if _, err := a.TokenFor(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	token, err := a.TokenFor(context.Background(), "acme/app")
	if err != nil {
		t.Fatalf("a failed renewal must fall back to the valid token: %v", err)
	}
	if token != "ghs_good" {
		t.Errorf("token = %q", token)
	}
}

func TestDiscoversInstallationPerRepository(t *testing.T) {
	_, pemKey := testKey(t)
	lookups := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			lookups[r.URL.Path]++
			id := 100
			if strings.Contains(r.URL.Path, "other") {
				id = 200
			}
			fmt.Fprintf(w, `{"id":%d,"account":{"login":"acme"}}`, id)
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"ghs_%s","expires_at":%q}`,
				strings.Split(r.URL.Path, "/")[3], time.Now().Add(time.Hour).Format(time.RFC3339))
		}
	}))
	defer srv.Close()

	a, _ := New(srv.URL, "1", pemKey, 0) // no fixed installation
	one, err := a.TokenFor(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.TokenFor(context.Background(), "other/app")
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Error("repositories in different installations must get different tokens")
	}
	// The installation lookup is cached.
	if _, err := a.TokenFor(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if n := lookups["/repos/acme/app/installation"]; n != 1 {
		t.Errorf("looked up the installation %d times, want 1", n)
	}
}

func TestErrorsAreActionable(t *testing.T) {
	_, pemKey := testKey(t)
	for status, want := range map[int]string{
		http.StatusUnauthorized: "app id",
		http.StatusNotFound:     "installed",
		http.StatusForbidden:    "Contents permission",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		a, _ := New(srv.URL, "1", pemKey, 7)
		_, err := a.TokenFor(context.Background(), "acme/app")
		if err == nil {
			t.Errorf("HTTP %d should be an error", status)
		} else if !strings.Contains(err.Error(), want) {
			t.Errorf("HTTP %d error = %v, want it to mention %q", status, err, want)
		}
		srv.Close()
	}
}

func TestRejectsUnusableKeys(t *testing.T) {
	cases := map[string][]byte{
		"not PEM":        []byte("just some text"),
		"empty":          nil,
		"wrong contents": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("nonsense")}),
	}
	for name, key := range cases {
		if _, err := New("https://api.github.com", "1", key, 0); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	if _, err := New("https://api.github.com", "", []byte("x"), 0); err == nil {
		t.Error("an empty app id should be refused")
	}
}

// Keys from key-management tooling often arrive as PKCS#8.
func TestAcceptsPKCS8Key(t *testing.T) {
	key, _ := testKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := New("https://api.github.com", "1", encoded, 1); err != nil {
		t.Errorf("a PKCS#8 key must be accepted: %v", err)
	}
}

// Endpoints such as /rate_limit concern no repository. With no fixed
// installation configured they must still work once one is known, rather than
// failing the way "deckhand doctor" did.
func TestRepositorylessRequestReusesAKnownInstallation(t *testing.T) {
	_, pemKey := testKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			fmt.Fprint(w, `{"id":77,"account":{"login":"acme"}}`)
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"ghs_x","expires_at":%q}`,
				time.Now().Add(time.Hour).Format(time.RFC3339))
		}
	}))
	defer srv.Close()

	a, _ := New(srv.URL, "1", pemKey, 0)

	// Before anything is known there is nothing to guess from.
	if _, err := a.TokenFor(context.Background(), ""); err == nil {
		t.Error("without any known installation this must fail, and say what to configure")
	} else if !strings.Contains(err.Error(), "installation_id") {
		t.Errorf("error = %v, want advice about installation_id", err)
	}

	// After a repository-scoped call, the installation is known and reusable.
	if _, err := a.TokenFor(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.TokenFor(context.Background(), ""); err != nil {
		t.Errorf("a known installation should answer for repositoryless endpoints: %v", err)
	}
}

// A configured installation id answers for everything, including endpoints
// with no repository.
func TestFixedInstallationCoversRepositorylessRequests(t *testing.T) {
	_, pemKey := testKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/installation") {
			t.Error("a configured installation must not be looked up")
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_fixed","expires_at":%q}`,
			time.Now().Add(time.Hour).Format(time.RFC3339))
	}))
	defer srv.Close()

	a, _ := New(srv.URL, "1", pemKey, 99)
	token, err := a.TokenFor(context.Background(), "")
	if err != nil || token != "ghs_fixed" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}
