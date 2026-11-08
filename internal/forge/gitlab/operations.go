package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ErrRejected marks a definite refusal: GitLab answered and the approval was
// not recorded (permission, conflict, validation). Transport errors and 5xx
// responses are never ErrRejected because the write may have applied.
var ErrRejected = errors.New("GitLab rejected approval")

var numeric = regexp.MustCompile(`^[1-9][0-9]*$`)

type userRef struct {
	ID int64 `json:"id"`
}

func ids(xs []userRef) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x.ID > 0 {
			out = append(out, fmt.Sprint(x.ID))
		}
	}
	return out
}

// CurrentUser returns the numeric ID of the user the credential acts as.
func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	var x userRef
	if e := c.request(ctx, "GET", "/user", nil, &x); e != nil {
		return "", e
	}
	if x.ID <= 0 {
		return "", errors.New("missing GitLab user identity")
	}
	return fmt.Sprint(x.ID), nil
}

type Pipeline struct {
	ID                                  int64
	ProjectID, SHA, Ref, Status, Source string
}

func (c *Client) Pipeline(ctx context.Context, projectID string, id int64) (Pipeline, error) {
	if !numeric.MatchString(projectID) || id <= 0 {
		return Pipeline{}, errors.New("invalid pipeline request")
	}
	var x struct {
		ID        int64  `json:"id"`
		ProjectID int64  `json:"project_id"`
		SHA       string `json:"sha"`
		Ref       string `json:"ref"`
		Status    string `json:"status"`
		Source    string `json:"source"`
	}
	if e := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/pipelines/%d", projectID, id), nil, &x); e != nil {
		return Pipeline{}, e
	}
	if x.ID != id || x.ProjectID <= 0 || !oid.MatchString(x.SHA) || x.Status == "" {
		return Pipeline{}, errors.New("invalid pipeline identity")
	}
	return Pipeline{ID: x.ID, ProjectID: fmt.Sprint(x.ProjectID), SHA: x.SHA, Ref: x.Ref, Status: x.Status, Source: x.Source}, nil
}

type Job struct {
	Name, Status string
	AllowFailure bool
	PipelineID   int64
}

// PipelineJobs lists the latest attempt of each job (GitLab omits retried
// attempts unless include_retried is set). Bridge/child pipelines are not jobs.
func (c *Client) PipelineJobs(ctx context.Context, projectID string, pipelineID int64, maxPages int) ([]Job, error) {
	if !numeric.MatchString(projectID) || pipelineID <= 0 || maxPages <= 0 || maxPages > 1000 {
		return nil, errors.New("invalid job request")
	}
	out := []Job{}
	for page := 1; page <= maxPages; page++ {
		var xs []struct {
			Name         string `json:"name"`
			Status       string `json:"status"`
			AllowFailure bool   `json:"allow_failure"`
			Pipeline     struct {
				ID        int64 `json:"id"`
				ProjectID int64 `json:"project_id"`
			} `json:"pipeline"`
		}
		if e := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/pipelines/%d/jobs?per_page=100&page=%d", projectID, pipelineID, page), nil, &xs); e != nil {
			return nil, e
		}
		for _, x := range xs {
			if x.Pipeline.ID != pipelineID || fmt.Sprint(x.Pipeline.ProjectID) != projectID || x.Name == "" {
				return nil, errors.New("job does not belong to requested pipeline")
			}
			out = append(out, Job{Name: x.Name, Status: x.Status, AllowFailure: x.AllowFailure, PipelineID: x.Pipeline.ID})
		}
		if len(xs) < 100 {
			return out, nil
		}
	}
	return nil, errors.New("job pagination cap exceeded")
}

// CommitParents returns the parent IDs of an immutable commit.
func (c *Client) CommitParents(ctx context.Context, projectID, sha string) ([]string, error) {
	if !numeric.MatchString(projectID) || !oid.MatchString(sha) {
		return nil, errors.New("invalid commit request")
	}
	var x struct {
		ID      string   `json:"id"`
		Parents []string `json:"parent_ids"`
	}
	if e := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/repository/commits/%s", projectID, sha), nil, &x); e != nil {
		return nil, e
	}
	if x.ID != sha {
		return nil, errors.New("commit identity mismatch")
	}
	for _, p := range x.Parents {
		if !oid.MatchString(p) {
			return nil, errors.New("invalid parent identity")
		}
	}
	return x.Parents, nil
}

type ProjectSettings struct {
	PipelineMustSucceed, AllowSkippedPipeline, MergedResultsPipelines, MergeTrains bool
	MergeMethod, CIConfigPath                                                      string
}

func (c *Client) ProjectSettings(ctx context.Context, id string) (ProjectSettings, error) {
	if !numeric.MatchString(id) {
		return ProjectSettings{}, errors.New("invalid project")
	}
	var x struct {
		ID                     int64  `json:"id"`
		PipelineMustSucceed    bool   `json:"only_allow_merge_if_pipeline_succeeds"`
		AllowSkipped           bool   `json:"allow_merge_on_skipped_pipeline"`
		MergeMethod            string `json:"merge_method"`
		MergedResultsPipelines bool   `json:"merge_pipelines_enabled"`
		MergeTrains            bool   `json:"merge_trains_enabled"`
		CIConfigPath           string `json:"ci_config_path"`
	}
	if e := c.request(ctx, "GET", "/projects/"+id, nil, &x); e != nil {
		return ProjectSettings{}, e
	}
	if fmt.Sprint(x.ID) != id {
		return ProjectSettings{}, errors.New("project identity mismatch")
	}
	return ProjectSettings{PipelineMustSucceed: x.PipelineMustSucceed, AllowSkippedPipeline: x.AllowSkipped, MergeMethod: x.MergeMethod, MergedResultsPipelines: x.MergedResultsPipelines, MergeTrains: x.MergeTrains, CIConfigPath: x.CIConfigPath}, nil
}

type ApprovalSettings struct{ ResetOnPush, SelectiveCodeOwnerRemovals bool }

// ApprovalSettings reads project merge request approval settings (Premium).
func (c *Client) ApprovalSettings(ctx context.Context, id string) (ApprovalSettings, error) {
	if !numeric.MatchString(id) {
		return ApprovalSettings{}, errors.New("invalid project")
	}
	var x struct {
		Reset     *bool `json:"reset_approvals_on_push"`
		Selective bool  `json:"selective_code_owner_removals"`
	}
	if e := c.request(ctx, "GET", "/projects/"+id+"/approvals", nil, &x); e != nil {
		return ApprovalSettings{}, e
	}
	if x.Reset == nil {
		return ApprovalSettings{}, errors.New("approval reset setting unavailable")
	}
	return ApprovalSettings{ResetOnPush: *x.Reset, SelectiveCodeOwnerRemovals: x.Selective}, nil
}

type ProtectedBranch struct{ Protected, AllowForcePush, CodeOwnerApprovalRequired bool }

// ProtectedBranch reports protection for an exact branch name. 404 is a known
// "not protected"; every other failure is an error.
func (c *Client) ProtectedBranch(ctx context.Context, id, branch string) (ProtectedBranch, error) {
	if !numeric.MatchString(id) || branch == "" || strings.ContainsAny(branch, "\x00\r\n") {
		return ProtectedBranch{}, errors.New("invalid protection request")
	}
	var x struct {
		Name           string `json:"name"`
		AllowForcePush bool   `json:"allow_force_push"`
		CodeOwner      bool   `json:"code_owner_approval_required"`
	}
	if e := c.request(ctx, "GET", "/projects/"+id+"/protected_branches/"+url.PathEscape(branch), nil, &x); e != nil {
		if statusCode(e) == 404 {
			return ProtectedBranch{}, nil
		}
		return ProtectedBranch{}, e
	}
	if x.Name != branch {
		return ProtectedBranch{}, errors.New("protected branch identity mismatch")
	}
	return ProtectedBranch{Protected: true, AllowForcePush: x.AllowForcePush, CodeOwnerApprovalRequired: x.CodeOwner}, nil
}

type ApprovalRule struct {
	Name, Type                 string
	Required                   int
	Approved                   bool
	EligibleIDs, ApprovedByIDs []string
}

// ApprovalState returns the effective rules for one merge request, including
// MR-level overrides and code owner rules (Premium).
func (c *Client) ApprovalState(ctx context.Context, id string, iid int) ([]ApprovalRule, error) {
	if !numeric.MatchString(id) || iid <= 0 {
		return nil, errors.New("invalid approval state request")
	}
	var x struct {
		Rules *[]struct {
			Name       string    `json:"name"`
			Type       string    `json:"rule_type"`
			Required   int       `json:"approvals_required"`
			Approved   bool      `json:"approved"`
			Eligible   []userRef `json:"eligible_approvers"`
			ApprovedBy []userRef `json:"approved_by"`
		} `json:"rules"`
	}
	if e := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/merge_requests/%d/approval_state", id, iid), nil, &x); e != nil {
		return nil, e
	}
	if x.Rules == nil {
		return nil, errors.New("approval rules unavailable")
	}
	out := []ApprovalRule{}
	for _, r := range *x.Rules {
		out = append(out, ApprovalRule{Name: r.Name, Type: r.Type, Required: r.Required, Approved: r.Approved, EligibleIDs: ids(r.Eligible), ApprovedByIDs: ids(r.ApprovedBy)})
	}
	return out, nil
}

type approvals struct {
	UserHasApproved bool `json:"user_has_approved"`
	ApprovedBy      []struct {
		User userRef `json:"user"`
	} `json:"approved_by"`
}

func (a approvals) has(bot string) bool {
	for _, x := range a.ApprovedBy {
		if fmt.Sprint(x.User.ID) == bot {
			return true
		}
	}
	return false
}

// Approve records the bot's approval fenced to head; GitLab answers 409 when
// the MR head no longer matches. Success means the bot appears in approved_by,
// not that the MR is fully approved. A 401 can mean "already approved", so the
// current approvals are re-read to reconcile before reporting a refusal. The
// returned ID is deterministic for (project, MR, head, bot).
func (c *Client) Approve(ctx context.Context, projectID string, number int, head, botID string) (string, error) {
	if !numeric.MatchString(projectID) || number <= 0 || !oid.MatchString(head) || !numeric.MatchString(botID) {
		return "", errors.New("immutable approval identity required")
	}
	id := fmt.Sprintf("gitlab:%s:%d:%s:%s", projectID, number, head, botID)
	b, _ := json.Marshal(map[string]string{"sha": head})
	var x approvals
	e := c.request(ctx, "POST", fmt.Sprintf("/projects/%s/merge_requests/%d/approve", projectID, number), bytes.NewReader(b), &x)
	switch code := statusCode(e); {
	case e == nil:
		if !x.has(botID) {
			return "", errors.New("ambiguous GitLab approval response")
		}
		return id, nil
	case code == 401:
		var now approvals
		if re := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/merge_requests/%d/approvals", projectID, number), nil, &now); re != nil {
			if rc := statusCode(re); rc >= 400 && rc < 500 {
				return "", fmt.Errorf("%w: unauthorized", ErrRejected)
			}
			return "", fmt.Errorf("approval outcome unknown: %w", re)
		}
		if now.UserHasApproved && now.has(botID) {
			return id, nil
		}
		return "", fmt.Errorf("%w: unauthorized", ErrRejected)
	case code == 403 || code == 404 || code == 409 || code == 422:
		return "", fmt.Errorf("%w: status %d", ErrRejected, code)
	default:
		return "", e
	}
}

type Comment struct{ ID, Body, AuthorID string }

func (c *Client) Comments(ctx context.Context, id string, number, maxPages int) ([]Comment, error) {
	if !project.MatchString(id) || id == "0" || number <= 0 || maxPages <= 0 || maxPages > 1000 {
		return nil, errors.New("invalid comment request")
	}
	out := []Comment{}
	for page := 1; page <= maxPages; page++ {
		var xs []struct {
			ID     int64
			Body   string
			System bool
			Author struct{ ID int64 }
		}
		if e := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/merge_requests/%d/notes?per_page=100&page=%d", id, number, page), nil, &xs); e != nil {
			return nil, e
		}
		for _, x := range xs {
			if x.ID <= 0 || x.Author.ID <= 0 {
				return nil, errors.New("invalid comment identity")
			}
			if x.System {
				continue
			}
			out = append(out, Comment{fmt.Sprint(x.ID), x.Body, fmt.Sprint(x.Author.ID)})
		}
		if len(xs) < 100 {
			return out, nil
		}
	}
	return nil, errors.New("comment pagination cap exceeded")
}

type writtenNote struct {
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	Author struct {
		ID int64 `json:"id"`
	} `json:"author"`
}

func (w writtenNote) comment() (Comment, error) {
	if w.ID <= 0 || w.Author.ID <= 0 {
		return Comment{}, errors.New("ambiguous note response")
	}
	return Comment{ID: fmt.Sprint(w.ID), Body: w.Body, AuthorID: fmt.Sprint(w.Author.ID)}, nil
}

func noteRejection(e error) error {
	switch statusCode(e) {
	case 401, 403, 404, 422:
		return fmt.Errorf("%w: status %d", ErrRejected, statusCode(e))
	}
	return e
}

// CreateNote posts a merge request note and returns it with its author.
func (c *Client) CreateNote(ctx context.Context, id string, number int, body string) (Comment, error) {
	if !numeric.MatchString(id) || number <= 0 || body == "" {
		return Comment{}, errors.New("invalid note request")
	}
	b, _ := json.Marshal(map[string]string{"body": body})
	var x writtenNote
	if e := c.request(ctx, "POST", fmt.Sprintf("/projects/%s/merge_requests/%d/notes", id, number), bytes.NewReader(b), &x); e != nil {
		return Comment{}, noteRejection(e)
	}
	return x.comment()
}

// UpdateNote replaces the body of an existing merge request note.
func (c *Client) UpdateNote(ctx context.Context, id string, number int, noteID, body string) (Comment, error) {
	if !numeric.MatchString(id) || number <= 0 || !numeric.MatchString(noteID) || body == "" {
		return Comment{}, errors.New("invalid note request")
	}
	b, _ := json.Marshal(map[string]string{"body": body})
	var x writtenNote
	if e := c.request(ctx, "PUT", fmt.Sprintf("/projects/%s/merge_requests/%d/notes/%s", id, number, noteID), bytes.NewReader(b), &x); e != nil {
		return Comment{}, noteRejection(e)
	}
	return x.comment()
}

// Unapprove withdraws the credential user's approval. GitLab answers 404 when
// the user has not approved, which is treated as already withdrawn.
func (c *Client) Unapprove(ctx context.Context, projectID string, number int) error {
	if !numeric.MatchString(projectID) || number <= 0 {
		return errors.New("invalid unapprove request")
	}
	e := c.request(ctx, "POST", fmt.Sprintf("/projects/%s/merge_requests/%d/unapprove", projectID, number), nil, nil)
	if statusCode(e) == 404 {
		return nil
	}
	return e
}

// ApprovedBy reports whether the user currently appears among the approvers.
func (c *Client) ApprovedBy(ctx context.Context, projectID string, number int, userID string) (bool, error) {
	if !numeric.MatchString(projectID) || number <= 0 || !numeric.MatchString(userID) {
		return false, errors.New("invalid approvals request")
	}
	var x approvals
	if e := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/merge_requests/%d/approvals", projectID, number), nil, &x); e != nil {
		return false, e
	}
	return x.has(userID), nil
}

// Project is a project's identity looked up by its full path.
type Project struct {
	ID, DefaultBranch, Path string
}

// ProjectByPath resolves a project path (group/sub/project) to its ID and
// default branch. The returned path must match the requested one.
func (c *Client) ProjectByPath(ctx context.Context, path string) (Project, error) {
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || part == "-" || strings.ContainsAny(part, "\x00\r\n?#%") {
			return Project{}, errors.New("invalid project path")
		}
	}
	var x struct {
		ID            int64  `json:"id"`
		DefaultBranch string `json:"default_branch"`
		Path          string `json:"path_with_namespace"`
	}
	if e := c.request(ctx, "GET", "/projects/"+url.PathEscape(path), nil, &x); e != nil {
		return Project{}, e
	}
	if x.ID <= 0 || x.DefaultBranch == "" || !strings.EqualFold(x.Path, path) {
		return Project{}, errors.New("project identity mismatch")
	}
	return Project{ID: fmt.Sprint(x.ID), DefaultBranch: x.DefaultBranch, Path: x.Path}, nil
}
