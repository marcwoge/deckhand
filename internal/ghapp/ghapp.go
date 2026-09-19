// Package ghapp authenticates as a GitHub App.
//
// A personal access token is a long-lived credential tied to a person: it
// expires, and when it does every deployment stops. A GitHub App instead holds
// a private key that never expires and mints installation tokens that live one
// hour, renewed automatically. The app is its own identity, so deployments do
// not break when someone leaves.
//
// The trade is that the private key is a long-lived secret on the machine, and
// it covers every installation of the app. It deserves the same care as an SSH
// host key: owned by the service account, mode 0600, never in the repository.
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
	"strings"
	"sync"
	"time"
)

// renewBefore is how long before expiry a token is replaced. Installation
// tokens last an hour; renewing with a margin means a deployment never starts
// with a credential that dies half way through.
const renewBefore = 10 * time.Minute

// Authenticator mints and caches installation tokens.
type Authenticator struct {
	appID string
	key   *rsa.PrivateKey
	api   string
	http  *http.Client

	// fixedInstallation is used for every repository when configured.
	fixedInstallation int64

	mu sync.Mutex
	// tokens caches one token per installation, and installations maps a
	// repository to the installation that covers it.
	tokens        map[int64]*cachedToken
	installations map[string]int64
}

type cachedToken struct {
	token   string
	expires time.Time
}

// New returns an Authenticator. appID is the app's ID (or client ID), pemKey
// the contents of its private key file, and installationID may be 0 to have it
// discovered per repository.
func New(api, appID string, pemKey []byte, installationID int64) (*Authenticator, error) {
	if strings.TrimSpace(appID) == "" {
		return nil, fmt.Errorf("app id is empty")
	}
	key, err := parsePrivateKey(pemKey)
	if err != nil {
		return nil, err
	}
	if api == "" {
		api = "https://api.github.com"
	}
	return &Authenticator{
		appID:             strings.TrimSpace(appID),
		key:               key,
		api:               strings.TrimRight(api, "/"),
		http:              &http.Client{Timeout: 30 * time.Second},
		fixedInstallation: installationID,
		tokens:            map[int64]*cachedToken{},
		installations:     map[string]int64{},
	}, nil
}

// parsePrivateKey accepts the PKCS#1 key GitHub hands out as well as PKCS#8,
// which some key management tools produce.
func parsePrivateKey(pemKey []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return nil, fmt.Errorf("private key is not PEM encoded; expected a file beginning with " +
			"\"-----BEGIN RSA PRIVATE KEY-----\"")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("private key could not be parsed as PKCS#1 or PKCS#8: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, but GitHub Apps use RSA", parsed)
	}
	return key, nil
}

// jwt builds the short-lived assertion that identifies the app itself. GitHub
// allows at most ten minutes; five leaves room for clock skew on both ends.
func (a *Authenticator) jwt(now time.Time) (string, error) {
	header := `{"alg":"RS256","typ":"JWT"}`
	claims := fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":%q}`,
		now.Add(-30*time.Second).Unix(), now.Add(5*time.Minute).Unix(), a.appID)

	enc := base64.RawURLEncoding
	signing := enc.EncodeToString([]byte(header)) + "." + enc.EncodeToString([]byte(claims))
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing the app assertion: %w", err)
	}
	return signing + "." + enc.EncodeToString(signature), nil
}

// TokenFor returns a valid installation token for a repository, minting or
// renewing one as needed. repo is "owner/name".
func (a *Authenticator) TokenFor(ctx context.Context, repo string) (string, error) {
	installation, err := a.installationFor(ctx, repo)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	cached := a.tokens[installation]
	a.mu.Unlock()
	if cached != nil && time.Until(cached.expires) > renewBefore {
		return cached.token, nil
	}

	token, expires, err := a.mintToken(ctx, installation)
	if err != nil {
		// A token that is still valid beats failing a deployment over a
		// temporary API problem.
		if cached != nil && time.Now().Before(cached.expires) {
			return cached.token, nil
		}
		return "", err
	}
	a.mu.Lock()
	a.tokens[installation] = &cachedToken{token: token, expires: expires}
	a.mu.Unlock()
	return token, nil
}

// installationFor resolves which installation covers a repository, caching the
// answer. A configured installation id is used for everything.
func (a *Authenticator) installationFor(ctx context.Context, repo string) (int64, error) {
	if a.fixedInstallation != 0 {
		return a.fixedInstallation, nil
	}
	a.mu.Lock()
	if id, ok := a.installations[repo]; ok {
		a.mu.Unlock()
		return id, nil
	}
	a.mu.Unlock()

	if repo == "" {
		// Endpoints such as /rate_limit concern no repository. Any installation
		// answers for them, so reuse one that is already known rather than
		// failing.
		a.mu.Lock()
		for _, id := range a.installations {
			a.mu.Unlock()
			return id, nil
		}
		a.mu.Unlock()
		return 0, fmt.Errorf("no installation known yet: set github.app.installation_id, " +
			"or make a repository-scoped request first")
	}
	var body struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := a.appRequest(ctx, http.MethodGet, "/repos/"+repo+"/installation", &body); err != nil {
		return 0, fmt.Errorf("finding the app installation for %s: %w", repo, err)
	}
	if body.ID == 0 {
		return 0, fmt.Errorf("the app is not installed on %s", repo)
	}
	a.mu.Lock()
	a.installations[repo] = body.ID
	a.mu.Unlock()
	return body.ID, nil
}

func (a *Authenticator) mintToken(ctx context.Context, installation int64) (string, time.Time, error) {
	var body struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	endpoint := fmt.Sprintf("/app/installations/%d/access_tokens", installation)
	if err := a.appRequest(ctx, http.MethodPost, endpoint, &body); err != nil {
		return "", time.Time{}, fmt.Errorf("requesting an installation token: %w", err)
	}
	if body.Token == "" {
		return "", time.Time{}, fmt.Errorf("GitHub returned an empty installation token")
	}
	if body.ExpiresAt.IsZero() {
		body.ExpiresAt = time.Now().Add(time.Hour)
	}
	return body.Token, body.ExpiresAt, nil
}

// appRequest calls the API authenticated as the app rather than as an
// installation.
func (a *Authenticator) appRequest(ctx context.Context, method, endpoint string, out interface{}) error {
	assertion, err := a.jwt(time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, a.api+endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+assertion)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "deckhand")

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return json.NewDecoder(resp.Body).Decode(out)
	case http.StatusUnauthorized:
		return fmt.Errorf("%s: GitHub rejected the app credentials; check the app id and "+
			"that the private key belongs to it", endpoint)
	case http.StatusNotFound:
		return fmt.Errorf("%s: not found; is the app installed, and does the installation "+
			"include this repository?", endpoint)
	case http.StatusForbidden:
		return fmt.Errorf("%s: forbidden; the app may lack the Contents permission", endpoint)
	default:
		var msg struct{ Message string }
		_ = json.NewDecoder(resp.Body).Decode(&msg)
		return fmt.Errorf("%s: HTTP %d %s", endpoint, resp.StatusCode, msg.Message)
	}
}

// Verify checks the credentials and reports the app's name, so a typo shows up
// at startup rather than at the first deployment.
func (a *Authenticator) Verify(ctx context.Context) (string, error) {
	var body struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := a.appRequest(ctx, http.MethodGet, "/app", &body); err != nil {
		return "", err
	}
	if body.Slug != "" {
		return body.Slug, nil
	}
	return body.Name, nil
}
