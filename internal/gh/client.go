// Package gh is a small, dependency-free GitHub API client tuned for polling:
// every request is conditional (ETag), so unchanged responses cost nothing
// against the rate limit.
package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const userAgent = "deckhand"

// Client talks to the GitHub REST API.
type Client struct {
	api   string
	token string
	http  *http.Client

	mu    sync.Mutex
	cache map[string]*cacheEntry
	// rateReset is the time the primary rate limit window resets, if exhausted.
	rateReset time.Time
}

type cacheEntry struct {
	etag string
	body []byte
}

// New returns a client. An empty token means unauthenticated access, which
// works for public repositories but is limited to 60 requests per hour.
func New(api, token string) *Client {
	return &Client{
		api:   strings.TrimRight(api, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
		cache: map[string]*cacheEntry{},
	}
}

// Authenticated reports whether a token is in use.
func (c *Client) Authenticated() bool { return c.token != "" }

// ErrRateLimited is returned when the primary rate limit is exhausted.
type ErrRateLimited struct{ Reset time.Time }

func (e *ErrRateLimited) Error() string {
	return fmt.Sprintf("GitHub rate limit exhausted, resets at %s", e.Reset.Format(time.RFC3339))
}

// get performs a conditional GET and decodes the (possibly cached) body.
func (c *Client) get(ctx context.Context, endpoint string, out interface{}) error {
	c.mu.Lock()
	if !c.rateReset.IsZero() && time.Now().Before(c.rateReset) {
		reset := c.rateReset
		c.mu.Unlock()
		return &ErrRateLimited{Reset: reset}
	}
	entry := c.cache[endpoint]
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.api+endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if entry != nil && entry.etag != "" {
		req.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", endpoint, redactURL(err.Error()))
	}
	defer resp.Body.Close()

	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining == "0" {
		if secs, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			c.mu.Lock()
			c.rateReset = time.Unix(secs, 0)
			c.mu.Unlock()
		}
	}

	switch resp.StatusCode {
	case http.StatusNotModified:
		if entry == nil {
			return fmt.Errorf("GET %s: got 304 without a cached body", endpoint)
		}
		return json.Unmarshal(entry.body, out)
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return err
		}
		if etag := resp.Header.Get("ETag"); etag != "" {
			c.mu.Lock()
			c.cache[endpoint] = &cacheEntry{etag: etag, body: body}
			c.mu.Unlock()
		}
		return json.Unmarshal(body, out)
	case http.StatusNotFound:
		return fmt.Errorf("GET %s: not found (repository missing, renamed, or the token lacks access)", endpoint)
	case http.StatusUnauthorized:
		return fmt.Errorf("GET %s: unauthorized (token invalid or expired)", endpoint)
	case http.StatusForbidden, http.StatusTooManyRequests:
		c.mu.Lock()
		reset := c.rateReset
		c.mu.Unlock()
		if !reset.IsZero() && time.Now().Before(reset) {
			return &ErrRateLimited{Reset: reset}
		}
		return fmt.Errorf("GET %s: forbidden (token lacks Contents:Read on this repository?)", endpoint)
	default:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: unexpected status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
}

// redactURL strips any token that a transport error might have echoed.
func redactURL(msg string) error {
	if i := strings.Index(msg, "Bearer "); i >= 0 {
		msg = msg[:i] + "Bearer [redacted]"
	}
	return fmt.Errorf("%s", msg)
}

// Target is the resolved thing a trigger points at.
type Target struct {
	SHA  string // full commit SHA
	Ref  string // tag name or branch name
	Kind string // release, tag or branch
	Name string // human readable: release name or commit subject
	URL  string
}

// Release mirrors the fields deckhand needs from a GitHub release.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
}

// LatestRelease returns the newest published release matching the filters.
func (c *Client) LatestRelease(ctx context.Context, repo, tagGlob string, allowPrerelease bool) (*Target, error) {
	var releases []Release
	if err := c.get(ctx, "/repos/"+repo+"/releases?per_page=30", &releases); err != nil {
		return nil, err
	}
	var best *Release
	for i := range releases {
		r := &releases[i]
		if r.Draft {
			continue
		}
		if r.Prerelease && !allowPrerelease {
			continue
		}
		if tagGlob != "" && tagGlob != "*" {
			if ok, _ := path.Match(tagGlob, r.TagName); !ok {
				continue
			}
		}
		if best == nil || r.PublishedAt.After(best.PublishedAt) {
			best = r
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no matching release found for %s", repo)
	}
	sha, err := c.resolveRef(ctx, repo, "tags/"+best.TagName)
	if err != nil {
		return nil, err
	}
	name := best.Name
	if name == "" {
		name = best.TagName
	}
	return &Target{SHA: sha, Ref: best.TagName, Kind: "release", Name: name, URL: best.HTMLURL}, nil
}

// LatestTag returns the highest tag matching the glob, compared by version
// order where possible and lexically otherwise.
func (c *Client) LatestTag(ctx context.Context, repo, glob string) (*Target, error) {
	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := c.get(ctx, "/repos/"+repo+"/tags?per_page=100", &tags); err != nil {
		return nil, err
	}
	best := -1
	for i := range tags {
		if glob != "" && glob != "*" {
			if ok, _ := path.Match(glob, tags[i].Name); !ok {
				continue
			}
		}
		if best < 0 || versionLess(tags[best].Name, tags[i].Name) {
			best = i
		}
	}
	if best < 0 {
		return nil, fmt.Errorf("no tag matching %q in %s", glob, repo)
	}
	sha, err := c.resolveRef(ctx, repo, "tags/"+tags[best].Name)
	if err != nil {
		// Annotated tags resolve via the refs API; lightweight ones already
		// carry the commit SHA in the tag listing.
		sha = tags[best].Commit.SHA
	}
	return &Target{SHA: sha, Ref: tags[best].Name, Kind: "tag", Name: tags[best].Name,
		URL: "https://github.com/" + repo + "/releases/tag/" + tags[best].Name}, nil
}

// BranchHead returns the current head commit of a branch.
func (c *Client) BranchHead(ctx context.Context, repo, branch string) (*Target, error) {
	var commit struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	if err := c.get(ctx, "/repos/"+repo+"/commits/"+branch, &commit); err != nil {
		return nil, err
	}
	subject := commit.Commit.Message
	if i := strings.IndexByte(subject, '\n'); i >= 0 {
		subject = subject[:i]
	}
	return &Target{SHA: commit.SHA, Ref: branch, Kind: "branch", Name: subject, URL: commit.HTMLURL}, nil
}

// resolveRef dereferences a ref (including annotated tags) to a commit SHA.
func (c *Client) resolveRef(ctx context.Context, repo, ref string) (string, error) {
	var obj struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := c.get(ctx, "/repos/"+repo+"/git/ref/"+ref, &obj); err != nil {
		return "", err
	}
	if obj.Object.Type != "tag" {
		return obj.Object.SHA, nil
	}
	var tag struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.get(ctx, "/repos/"+repo+"/git/tags/"+obj.Object.SHA, &tag); err != nil {
		return "", err
	}
	return tag.Object.SHA, nil
}

// versionLess compares two tags such that "v1.10.0" sorts above "v1.9.0".
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

// RateLimitStatus is the current standing with the API.
type RateLimitStatus struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

// RateLimit reports the current rate limit. The endpoint itself is free, and
// it doubles as a way to check that a token is valid.
func (c *Client) RateLimit(ctx context.Context) (*RateLimitStatus, error) {
	var body struct {
		Resources struct {
			Core struct {
				Limit     int   `json:"limit"`
				Remaining int   `json:"remaining"`
				Reset     int64 `json:"reset"`
			} `json:"core"`
		} `json:"resources"`
	}
	if err := c.get(ctx, "/rate_limit", &body); err != nil {
		return nil, err
	}
	core := body.Resources.Core
	return &RateLimitStatus{Limit: core.Limit, Remaining: core.Remaining,
		Reset: time.Unix(core.Reset, 0)}, nil
}

// Repository reports whether a repository can be read with the current
// credentials, and whether it is private.
func (c *Client) Repository(ctx context.Context, repo string) (private bool, err error) {
	var body struct {
		Private  bool `json:"private"`
		Archived bool `json:"archived"`
	}
	if err := c.get(ctx, "/repos/"+repo, &body); err != nil {
		return false, err
	}
	return body.Private, nil
}
