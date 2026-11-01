package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (c *Client) request(ctx context.Context, method, path string, input, out any) error {
	u, e := url.Parse(c.base)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("invalid API URL")
	}
	var body io.Reader
	if input != nil {
		b, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if e != nil {
		return e
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if hc.Timeout <= 0 {
		hc.Timeout = 30 * time.Second
	}
	res, e := hc.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub status %d", res.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, c.max+1))
	if e != nil {
		return e
	}
	if int64(len(b)) > c.max {
		return errors.New("response exceeds byte cap")
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}
func repoPath(owner, repo string) (string, error) {
	if !segment.MatchString(owner) || !segment.MatchString(repo) || owner == "." || owner == ".." || repo == "." || repo == ".." {
		return "", errors.New("invalid repository")
	}
	return "/repos/" + owner + "/" + repo, nil
}

// Checks returns all bounded pages, failing rather than silently dropping checks.
func (c *Client) Checks(ctx context.Context, owner, repo, revision string, maxPages, pageSize int) ([]Check, error) {
	p, e := repoPath(owner, repo)
	if e != nil {
		return nil, e
	}
	if !oid.MatchString(revision) || maxPages <= 0 || maxPages > 1000 || pageSize <= 0 || pageSize > 100 {
		return nil, errors.New("invalid check limits/revision")
	}
	out := []Check{}
	for page := 1; page <= maxPages; page++ {
		var x struct {
			Total int `json:"total_count"`
			Runs  []struct {
				Name       string
				Head       string `json:"head_sha"`
				Conclusion string
				App        struct{ ID int64 }
			} `json:"check_runs"`
		}
		if e = c.request(ctx, "GET", fmt.Sprintf("%s/commits/%s/check-runs?per_page=%d&page=%d", p, revision, pageSize, page), nil, &x); e != nil {
			return nil, e
		}
		for _, r := range x.Runs {
			if r.Head != revision || r.App.ID <= 0 {
				return nil, errors.New("unverified check identity")
			}
			out = append(out, Check{r.Name, strconv.FormatInt(r.App.ID, 10), r.Head, r.Conclusion})
		}
		if len(out) >= x.Total && x.Total >= 0 {
			return out, nil
		}
		if len(x.Runs) == 0 {
			return nil, errors.New("incomplete check pagination")
		}
	}
	return nil, errors.New("check pagination cap exceeded")
}

type Protection struct {
	Known, RequiredReviews, StrictChecks bool
	RequiredContexts                     []string
	Reason                               string
}

func (c *Client) Protection(ctx context.Context, owner, repo, branch string) (Protection, error) {
	p, e := repoPath(owner, repo)
	if e != nil {
		return Protection{}, e
	}
	if branch == "" || strings.ContainsAny(branch, "\x00\r\n") {
		return Protection{}, errors.New("branch required")
	}
	var x struct {
		Checks *struct {
			Strict   bool
			Contexts []string
		} `json:"required_status_checks"`
		Reviews *struct {
			Count int `json:"required_approving_review_count"`
		} `json:"required_pull_request_reviews"`
	}
	if e = c.request(ctx, "GET", p+"/branches/"+url.PathEscape(branch)+"/protection", nil, &x); e != nil {
		return Protection{Reason: "branch protection unavailable"}, e
	}
	r := Protection{Known: true, RequiredReviews: x.Reviews != nil && x.Reviews.Count > 0}
	if x.Checks != nil {
		r.StrictChecks = x.Checks.Strict
		r.RequiredContexts = x.Checks.Contexts
	}
	return r, nil
}

// Approve is a revision-bound transport primitive. Only host publication code
// may call it after policy/check/native-protection and job generation validation.
func (c *Client) Approve(ctx context.Context, owner, repo string, number int, head string) (string, error) {
	p, e := repoPath(owner, repo)
	if e != nil {
		return "", e
	}
	if number <= 0 || !oid.MatchString(head) {
		return "", errors.New("immutable approval revision required")
	}
	var x struct{ ID int64 }
	e = c.request(ctx, "POST", fmt.Sprintf("%s/pulls/%d/reviews", p, number), map[string]any{"commit_id": head, "event": "APPROVE", "body": "reviewd: validated host policy approval"}, &x)
	if e != nil {
		return "", e
	}
	if x.ID <= 0 {
		return "", errors.New("ambiguous approval response")
	}
	return fmt.Sprint(x.ID), nil
}

type Comment struct{ ID, Body, AuthorID string }

func (c *Client) Comments(ctx context.Context, owner, repo string, number, maxPages int) ([]Comment, error) {
	p, e := repoPath(owner, repo)
	if e != nil {
		return nil, e
	}
	if number <= 0 || maxPages <= 0 || maxPages > 1000 {
		return nil, errors.New("invalid comment limits")
	}
	out := []Comment{}
	for page := 1; page <= maxPages; page++ {
		var xs []struct {
			ID   int64
			Body string
			User struct{ ID int64 }
		}
		if e = c.request(ctx, "GET", fmt.Sprintf("%s/issues/%d/comments?per_page=100&page=%d", p, number, page), nil, &xs); e != nil {
			return nil, e
		}
		for _, x := range xs {
			if x.ID <= 0 || x.User.ID <= 0 {
				return nil, errors.New("invalid comment identity")
			}
			out = append(out, Comment{fmt.Sprint(x.ID), x.Body, fmt.Sprint(x.User.ID)})
		}
		if len(xs) < 100 {
			return out, nil
		}
	}
	return nil, errors.New("comment pagination cap exceeded")
}

// UpsertComment uses a trusted bot account ID and stable marker. Unknown write
// completion must be reconciled with Comments before the host retries.
func (c *Client) UpsertComment(ctx context.Context, owner, repo string, number int, botID, marker, body string) (string, error) {
	if botID == "" || marker == "" || !strings.Contains(body, marker) {
		return "", errors.New("owned comment identity required")
	}
	cs, e := c.Comments(ctx, owner, repo, number, 10)
	if e != nil {
		return "", e
	}
	id := ""
	for _, x := range cs {
		if x.AuthorID == botID && strings.Contains(x.Body, marker) {
			if id != "" {
				return "", errors.New("ambiguous owned comments")
			}
			id = x.ID
		}
	}
	p, _ := repoPath(owner, repo)
	path := fmt.Sprintf("%s/issues/%d/comments", p, number)
	method := "POST"
	if id != "" {
		path = p + "/issues/comments/" + id
		method = "PATCH"
	}
	var x struct{ ID int64 }
	if e = c.request(ctx, method, path, map[string]string{"body": body}, &x); e != nil {
		return "", e
	}
	if x.ID <= 0 {
		return "", errors.New("ambiguous comment response")
	}
	return fmt.Sprint(x.ID), nil
}
