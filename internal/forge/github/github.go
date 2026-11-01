// Package github contains bounded read-only GitHub API adapters.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Client struct {
	base, token string
	http        *http.Client
	max         int64
}

func NewClient(baseURL, token string, httpClient *http.Client, maxBytes int64) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return &Client{strings.TrimRight(baseURL, "/"), token, httpClient, maxBytes}
}

type Change struct{ RepositoryID, SourceRepositoryID, HeadOID, TargetOID string }

var segment = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var oid = regexp.MustCompile(`^[A-Fa-f0-9]+$`)

func (c *Client) Change(ctx context.Context, owner, repo string, number int) (Change, error) {
	if !segment.MatchString(owner) || !segment.MatchString(repo) || owner == "." || owner == ".." || repo == "." || repo == ".." || number <= 0 {
		return Change{}, errors.New("invalid repository/change identity")
	}
	base, e := url.Parse(c.base)
	if e != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return Change{}, errors.New("invalid API base URL")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.base, owner, repo, number), nil)
	if e != nil {
		return Change{}, e
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// Do not follow redirects with a credential or to an untrusted API host.
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if hc.Timeout <= 0 {
		hc.Timeout = 30 * time.Second
	}
	res, e := hc.Do(req)
	if e != nil {
		return Change{}, fmt.Errorf("GitHub request failed: %w", e)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Change{}, fmt.Errorf("GitHub HTTP status %d", res.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, c.max+1))
	if e != nil {
		return Change{}, e
	}
	if int64(len(b)) > c.max {
		return Change{}, errors.New("GitHub response exceeds byte cap")
	}
	type ref struct {
		SHA  string `json:"sha"`
		Repo *struct {
			ID json.Number `json:"id"`
		} `json:"repo"`
	}
	var raw struct {
		ID         json.Number `json:"id"`
		Head, Base ref
	}
	if e = json.Unmarshal(b, &raw); e != nil {
		return Change{}, errors.New("invalid GitHub response JSON")
	}
	validID := func(n json.Number) bool { i, e := n.Int64(); return e == nil && i > 0 }
	if !validID(raw.ID) || raw.Head.Repo == nil || raw.Base.Repo == nil || !validID(raw.Head.Repo.ID) || !validID(raw.Base.Repo.ID) || !oid.MatchString(raw.Head.SHA) || !oid.MatchString(raw.Base.SHA) {
		return Change{}, errors.New("missing or invalid immutable source identity")
	}
	return Change{RepositoryID: raw.Base.Repo.ID.String(), SourceRepositoryID: raw.Head.Repo.ID.String(), HeadOID: raw.Head.SHA, TargetOID: raw.Base.SHA}, nil
}

type Check struct{ Name, Producer, OID, Conclusion string }

func TrustedCheck(c Check, expectedProducer, expectedOID string) bool {
	return expectedProducer != "" && expectedOID != "" && c.Producer == expectedProducer && c.OID == expectedOID && c.Conclusion == "success"
}
