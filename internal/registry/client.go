// Package registry reads image digests from an OCI container registry.
//
// It exists so a production machine can watch an image rather than a git
// repository: the build happens elsewhere - CI, a build host, anywhere - pushes
// an image, and deckhand notices the new digest and restarts the service. No
// compiler, no source tree and no build tooling on the machine that serves
// traffic.
//
// Only the read paths of the registry API are implemented, and only pull scope
// is ever requested.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// acceptedManifests lists the media types worth asking for. A multi-arch image
// answers with an index; a single-arch one with a plain manifest. Sending all of
// them means the digest we get back is the one `docker pull` would use.
var acceptedManifests = []string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}

// Client talks to one registry.
type Client struct {
	http *http.Client

	// credentials are looked up per registry host.
	mu     sync.Mutex
	tokens map[string]*cachedToken
	creds  map[string]Credential
}

// Credential is a username and password or token for a registry host.
type Credential struct {
	Username string
	Password string
}

type cachedToken struct {
	token   string
	expires time.Time
}

// New returns a client. creds maps a registry host to its credentials; a host
// that is absent is accessed anonymously, which is enough for public images.
func New(creds map[string]Credential) *Client {
	if creds == nil {
		creds = map[string]Credential{}
	}
	return &Client{
		http:   &http.Client{Timeout: 30 * time.Second},
		tokens: map[string]*cachedToken{},
		creds:  creds,
	}
}

// Reference is a parsed image reference such as
// "ghcr.io/marcwoge/app:latest".
type Reference struct {
	Registry string // ghcr.io
	Name     string // marcwoge/app
	Tag      string // latest
	// Scheme is https except for a plain-HTTP registry, which only makes sense
	// for a local one.
	Scheme string
}

// String renders the reference without the digest.
func (r Reference) String() string { return r.Registry + "/" + r.Name + ":" + r.Tag }

// baseURL is the registry API root.
func (r Reference) baseURL() string { return r.Scheme + "://" + r.Registry + "/v2" }

// ParseReference splits an image reference. The tag defaults to "latest", and a
// reference without a registry is taken to mean Docker Hub, as docker does.
func ParseReference(image string) (Reference, error) {
	ref := Reference{Scheme: "https"}
	if image == "" {
		return ref, fmt.Errorf("image reference is empty")
	}
	if strings.Contains(image, "@") {
		return ref, fmt.Errorf("image %q already pins a digest; deckhand watches a tag "+
			"and resolves the digest itself", image)
	}

	rest := image
	// A leading component is a registry when it looks like a host: it contains a
	// dot or a colon, or is exactly "localhost".
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		head := rest[:i]
		if strings.ContainsAny(head, ".:") || head == "localhost" {
			ref.Registry = head
			rest = rest[i+1:]
		}
	}
	if ref.Registry == "" {
		ref.Registry = "registry-1.docker.io"
		// Docker Hub keeps unqualified names under "library".
		if !strings.Contains(rest, "/") {
			rest = "library/" + rest
		}
	}
	if strings.HasPrefix(ref.Registry, "localhost") || strings.HasPrefix(ref.Registry, "127.0.0.1") {
		ref.Scheme = "http"
	}

	name, tag := rest, "latest"
	// The tag is after the last colon, but only if no slash follows it - a port
	// in a registry host has already been split off by now.
	if i := strings.LastIndexByte(rest, ':'); i >= 0 && !strings.Contains(rest[i:], "/") {
		name, tag = rest[:i], rest[i+1:]
	}
	if name == "" {
		return ref, fmt.Errorf("image %q has no repository name", image)
	}
	ref.Name, ref.Tag = name, tag
	return ref, nil
}

// Digest returns the manifest digest a tag currently points at. This is the
// value that changes when a new image is pushed, even when the tag does not.
func (c *Client) Digest(ctx context.Context, ref Reference) (string, error) {
	endpoint := ref.baseURL() + "/" + ref.Name + "/manifests/" + url.PathEscape(ref.Tag)
	resp, err := c.request(ctx, http.MethodHead, endpoint, ref, acceptedManifests)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		// Some registries omit the header on HEAD; ask for the body instead.
		resp2, err := c.request(ctx, http.MethodGet, endpoint, ref, acceptedManifests)
		if err != nil {
			return "", err
		}
		defer resp2.Body.Close()
		digest = resp2.Header.Get("Docker-Content-Digest")
	}
	if digest == "" {
		return "", fmt.Errorf("%s: the registry returned no digest for tag %q", ref.Registry, ref.Tag)
	}
	return digest, nil
}

// Tags lists the tags of a repository, newest-looking last.
func (c *Client) Tags(ctx context.Context, ref Reference) ([]string, error) {
	endpoint := ref.baseURL() + "/" + ref.Name + "/tags/list?n=200"
	resp, err := c.request(ctx, http.MethodGet, endpoint, ref, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s: unreadable tag list: %w", ref.Registry, err)
	}
	sort.Strings(body.Tags)
	return body.Tags, nil
}

// MatchTag returns the highest tag matching a glob, compared by version order
// where possible.
func (c *Client) MatchTag(ctx context.Context, ref Reference, glob string) (string, error) {
	tags, err := c.Tags(ctx, ref)
	if err != nil {
		return "", err
	}
	best := ""
	for _, tag := range tags {
		if glob != "" && glob != "*" {
			if ok, _ := path.Match(glob, tag); !ok {
				continue
			}
		}
		if best == "" || versionLess(best, tag) {
			best = tag
		}
	}
	if best == "" {
		return "", fmt.Errorf("no tag matching %q in %s/%s", glob, ref.Registry, ref.Name)
	}
	return best, nil
}

// request performs an authenticated registry request, obtaining a token on the
// first challenge.
func (c *Client) request(ctx context.Context, method, endpoint string, ref Reference,
	accept []string) (*http.Response, error) {

	do := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
		if err != nil {
			return nil, err
		}
		for _, a := range accept {
			req.Header.Add("Accept", a)
		}
		req.Header.Set("User-Agent", "deckhand")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return c.http.Do(req)
	}

	resp, err := do(c.cachedToken(ref.Registry))
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, ref.Registry, redact(err))
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return c.checkStatus(resp, ref)
	}

	// Answer the challenge and retry once.
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	token, err := c.fetchToken(ctx, ref, challenge)
	if err != nil {
		return nil, err
	}
	resp, err = do(token)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, ref.Registry, redact(err))
	}
	return c.checkStatus(resp, ref)
}

func (c *Client) checkStatus(resp *http.Response, ref Reference) (*http.Response, error) {
	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, fmt.Errorf("%s/%s:%s not found; check the image name and tag",
			ref.Registry, ref.Name, ref.Tag)
	case http.StatusUnauthorized, http.StatusForbidden:
		resp.Body.Close()
		return nil, fmt.Errorf("%s/%s: not authorised; a private image needs registry "+
			"credentials with pull access", ref.Registry, ref.Name)
	case http.StatusTooManyRequests:
		resp.Body.Close()
		return nil, fmt.Errorf("%s: rate limited; raise poll_interval (Docker Hub allows "+
			"few anonymous requests)", ref.Registry)
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("%s/%s: HTTP %d", ref.Registry, ref.Name, resp.StatusCode)
	}
}

func (c *Client) cachedToken(host string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.tokens[host]; ok && time.Until(t.expires) > time.Minute {
		return t.token
	}
	return ""
}

// fetchToken follows a Bearer challenge. Registries hand out short-lived tokens
// scoped to one repository and pull access only.
func (c *Client) fetchToken(ctx context.Context, ref Reference, challenge string) (string, error) {
	realm, service, scope := parseChallenge(challenge)
	if realm == "" {
		return "", fmt.Errorf("%s: unauthorised and no Bearer challenge to answer", ref.Registry)
	}
	if scope == "" {
		scope = "repository:" + ref.Name + ":pull"
	}

	endpoint, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("%s: unusable auth realm %q", ref.Registry, realm)
	}
	q := endpoint.Query()
	q.Set("scope", scope)
	if service != "" {
		q.Set("service", service)
	}
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "deckhand")
	c.mu.Lock()
	cred, hasCred := c.creds[ref.Registry]
	c.mu.Unlock()
	if hasCred && cred.Password != "" {
		user := cred.Username
		if user == "" {
			user = "deckhand"
		}
		req.SetBasicAuth(user, cred.Password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: requesting a registry token: %w", ref.Registry, redact(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if hasCred {
			return "", fmt.Errorf("%s: the registry rejected the configured credentials (HTTP %d)",
				ref.Registry, resp.StatusCode)
		}
		return "", fmt.Errorf("%s: anonymous access refused (HTTP %d); configure credentials "+
			"for a private image", ref.Registry, resp.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("%s: unreadable token response: %w", ref.Registry, err)
	}
	token := body.Token
	if token == "" {
		token = body.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("%s: the registry returned an empty token", ref.Registry)
	}
	lifetime := time.Duration(body.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = 5 * time.Minute
	}
	c.mu.Lock()
	c.tokens[ref.Registry] = &cachedToken{token: token, expires: time.Now().Add(lifetime)}
	c.mu.Unlock()
	return token, nil
}

// parseChallenge picks realm, service and scope out of a WWW-Authenticate
// header such as: Bearer realm="https://ghcr.io/token",service="ghcr.io"
func parseChallenge(header string) (realm, service, scope string) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(header), "Bearer"))
	for _, part := range splitParams(rest) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "realm":
			realm = value
		case "service":
			service = value
		case "scope":
			scope = value
		}
	}
	return realm, service, scope
}

// splitParams splits on commas that are not inside quotes.
func splitParams(s string) []string {
	var out []string
	var current strings.Builder
	inQuotes := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			current.WriteRune(r)
		case r == ',' && !inQuotes:
			out = append(out, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

// redact keeps a credential out of a transport error.
func redact(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, "Bearer "); i >= 0 {
		msg = msg[:i] + "Bearer [redacted]"
	}
	return fmt.Errorf("%s", msg)
}

// versionLess compares tags so that "1.10.0" sorts above "1.9.0".
func versionLess(a, b string) bool {
	as, bs := splitVersion(a), splitVersion(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	if len(as) != len(bs) {
		return len(as) < len(bs)
	}
	return a < b
}

func splitVersion(s string) []int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var out []int
	cur, has := 0, false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur = cur*10 + int(r-'0')
			has = true
			continue
		}
		if has {
			out = append(out, cur)
			cur, has = 0, false
		}
	}
	if has {
		out = append(out, cur)
	}
	return out
}
