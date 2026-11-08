package gitlab

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
	bearer      bool
	http        *http.Client
	max         int64
}

// NewClient authenticates with a personal, project or group access token sent
// as PRIVATE-TOKEN. An empty token sends no credential header.
func NewClient(base, token string, h *http.Client, max int64) *Client {
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	if max <= 0 {
		max = 1 << 20
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: h, max: max}
}

// NewBearerClient authenticates with an OAuth2-style Authorization: Bearer
// header, which GitLab also accepts for personal/project/group access tokens.
func NewBearerClient(base, token string, h *http.Client, max int64) *Client {
	c := NewClient(base, token, h, max)
	c.bearer = true
	return c
}

// StatusError reports a non-2xx GitLab response. Callers distinguish definite
// refusals (4xx) from ambiguous outcomes (5xx, transport errors).
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("GitLab status %d", e.Code) }

func statusCode(e error) int {
	var se *StatusError
	if errors.As(e, &se) {
		return se.Code
	}
	return 0
}

// Change is the live merge request identity. TargetOID is the live target
// branch tip, not the possibly stale diff start.
type Change struct {
	RepositoryID, SourceRepositoryID, HeadOID, TargetOID, MergeBaseOID string
	TargetBranch, State                                                string
	Draft                                                              bool
	HeadPipelineID                                                     int64
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader, out any) error {
	u, e := url.Parse(c.base)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid API URL")
	}
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if e != nil {
		return e
	}
	if c.token != "" {
		if c.bearer {
			req.Header.Set("Authorization", "Bearer "+c.token)
		} else {
			req.Header.Set("PRIVATE-TOKEN", c.token)
		}
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
		return &StatusError{Code: res.StatusCode}
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, c.max+1))
	if e != nil {
		return e
	}
	if int64(len(b)) > c.max {
		return errors.New("response limit exceeded")
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

var project = regexp.MustCompile(`^[0-9]+$`)
var oid = regexp.MustCompile(`^[a-fA-F0-9]+$`)

func (c *Client) Change(ctx context.Context, id string, number int) (Change, error) {
	if !project.MatchString(id) || id == "0" || number <= 0 {
		return Change{}, errors.New("numeric project ID and MR number required")
	}
	var x struct {
		ID, IID, ProjectID, SourceProjectID, TargetProjectID int64
		SHA                                                  string
		DiffRefs                                             struct{ HeadSHA, BaseSHA, StartSHA string } `json:"diff_refs"`
	} // Explicit wire tags avoid underscore-name ambiguity.
	var wire struct {
		ID              int64  `json:"id"`
		IID             int64  `json:"iid"`
		ProjectID       int64  `json:"project_id"`
		SourceProjectID int64  `json:"source_project_id"`
		TargetProjectID int64  `json:"target_project_id"`
		SHA             string `json:"sha"`
		TargetBranch    string `json:"target_branch"`
		State           string `json:"state"`
		Draft           bool   `json:"draft"`
		WIP             bool   `json:"work_in_progress"`
		HeadPipeline    *struct {
			ID int64 `json:"id"`
		} `json:"head_pipeline"`
		DiffRefs struct {
			HeadSHA  string `json:"head_sha"`
			BaseSHA  string `json:"base_sha"`
			StartSHA string `json:"start_sha"`
		} `json:"diff_refs"`
	}
	if e := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/merge_requests/%d", id, number), nil, &wire); e != nil {
		return Change{}, e
	}
	x.ID = wire.ID
	x.IID = wire.IID
	x.ProjectID = wire.ProjectID
	x.SourceProjectID = wire.SourceProjectID
	x.TargetProjectID = wire.TargetProjectID
	x.SHA = wire.SHA
	x.DiffRefs.HeadSHA = wire.DiffRefs.HeadSHA
	x.DiffRefs.BaseSHA = wire.DiffRefs.BaseSHA
	x.DiffRefs.StartSHA = wire.DiffRefs.StartSHA
	if x.ID <= 0 || x.IID != int64(number) || x.SourceProjectID <= 0 || x.TargetProjectID <= 0 || fmt.Sprint(x.TargetProjectID) != id || !oid.MatchString(x.SHA) || x.DiffRefs.HeadSHA != x.SHA || !oid.MatchString(x.DiffRefs.StartSHA) || !oid.MatchString(x.DiffRefs.BaseSHA) {
		return Change{}, errors.New("missing immutable merge request identity")
	}
	if wire.TargetBranch == "" || strings.ContainsAny(wire.TargetBranch, "\x00\r\n") {
		return Change{}, errors.New("target branch unavailable")
	}
	var branch struct {
		Name   string
		Commit struct{ ID string }
	}
	if err := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/repository/branches/%s", id, url.PathEscape(wire.TargetBranch)), nil, &branch); err != nil {
		return Change{}, err
	}
	if branch.Name != wire.TargetBranch || !oid.MatchString(branch.Commit.ID) {
		return Change{}, errors.New("unverified target branch revision")
	}
	out := Change{RepositoryID: fmt.Sprint(x.TargetProjectID), SourceRepositoryID: fmt.Sprint(x.SourceProjectID), HeadOID: x.SHA, TargetOID: branch.Commit.ID, MergeBaseOID: x.DiffRefs.BaseSHA,
		TargetBranch: wire.TargetBranch, State: wire.State, Draft: wire.Draft || wire.WIP}
	if wire.HeadPipeline != nil {
		if wire.HeadPipeline.ID <= 0 {
			return Change{}, errors.New("invalid head pipeline identity")
		}
		out.HeadPipelineID = wire.HeadPipeline.ID
	}
	return out, nil
}
