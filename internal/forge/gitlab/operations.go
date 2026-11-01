package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Approve binds the request to the expected MR head; GitLab rejects a SHA mismatch.
// Callers still enforce live policy, target, trusted checks and native protections.
func (c *Client) Approve(ctx context.Context, projectID string, number int, head string) (string, error) {
	if !project.MatchString(projectID) || projectID == "0" || number <= 0 || !oid.MatchString(head) {
		return "", errors.New("immutable approval identity required")
	}
	b, _ := json.Marshal(map[string]string{"sha": head})
	var x struct {
		ID       int64
		Approved bool
	}
	if e := c.request(ctx, "POST", fmt.Sprintf("/projects/%s/merge_requests/%d/approve", projectID, number), bytes.NewReader(b), &x); e != nil {
		return "", e
	}
	if x.ID <= 0 || !x.Approved {
		return "", errors.New("ambiguous GitLab approval response")
	}
	return fmt.Sprint(x.ID), nil
}

// Checks reads candidate commit statuses with bounded pagination. Producer is
// GitLab's external_id, which must be configured as a trusted identity; missing
// external identity remains unknown rather than trusting a display name.
func (c *Client) Checks(ctx context.Context, id, revision string, maxPages, pageSize int) ([]Check, error) {
	if !project.MatchString(id) || id == "0" || !oid.MatchString(revision) || maxPages <= 0 || maxPages > 1000 || pageSize <= 0 || pageSize > 100 {
		return nil, errors.New("invalid check request")
	}
	out := []Check{}
	for page := 1; page <= maxPages; page++ {
		var xs []struct {
			Name, SHA, Status string
			ExternalID        string `json:"external_id"`
		}
		if e := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/repository/commits/%s/statuses?per_page=%d&page=%d", id, revision, pageSize, page), nil, &xs); e != nil {
			return nil, e
		}
		for _, x := range xs {
			if x.SHA != revision {
				return nil, errors.New("check revision mismatch")
			}
			out = append(out, Check{x.Name, x.ExternalID, x.SHA, x.Status})
		}
		if len(xs) < pageSize {
			return out, nil
		}
	}
	return nil, errors.New("check pagination cap exceeded")
}

type Protection struct {
	Known, Protected, ApprovalRulesKnown bool
	RequiredApprovals                    int
	Reason                               string
}

func (c *Client) Protection(ctx context.Context, id, branch string) (Protection, error) {
	if !project.MatchString(id) || id == "0" || branch == "" || strings.ContainsAny(branch, "\x00\r\n") {
		return Protection{}, errors.New("invalid protection request")
	}
	var x struct{ Protected bool }
	if e := c.request(ctx, "GET", "/projects/"+id+"/repository/branches/"+url.PathEscape(branch), nil, &x); e != nil {
		return Protection{Reason: "branch protection unavailable"}, e
	}
	var rules []struct {
		ApprovalsRequired int `json:"approvals_required"`
	}
	if e := c.request(ctx, "GET", "/projects/"+id+"/approval_rules?per_page=100", nil, &rules); e != nil {
		return Protection{Known: true, Protected: x.Protected, Reason: "approval rules unavailable"}, e
	}
	if len(rules) >= 100 {
		return Protection{}, errors.New("approval rules pagination incomplete")
	}
	n := 0
	for _, r := range rules {
		if r.ApprovalsRequired > n {
			n = r.ApprovalsRequired
		}
	}
	return Protection{Known: true, Protected: x.Protected, ApprovalRulesKnown: true, RequiredApprovals: n}, nil
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
			Author struct{ ID int64 }
		}
		if e := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/merge_requests/%d/notes?per_page=100&page=%d", id, number, page), nil, &xs); e != nil {
			return nil, e
		}
		for _, x := range xs {
			if x.ID <= 0 || x.Author.ID <= 0 {
				return nil, errors.New("invalid comment identity")
			}
			out = append(out, Comment{fmt.Sprint(x.ID), x.Body, fmt.Sprint(x.Author.ID)})
		}
		if len(xs) < 100 {
			return out, nil
		}
	}
	return nil, errors.New("comment pagination cap exceeded")
}
func (c *Client) UpsertComment(ctx context.Context, id string, number int, botID, marker, body string) (string, error) {
	if botID == "" || marker == "" || !strings.Contains(body, marker) {
		return "", errors.New("owned comment identity required")
	}
	cs, e := c.Comments(ctx, id, number, 10)
	if e != nil {
		return "", e
	}
	note := ""
	for _, x := range cs {
		if x.AuthorID == botID && strings.Contains(x.Body, marker) {
			if note != "" {
				return "", errors.New("ambiguous owned comments")
			}
			note = x.ID
		}
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/notes", id, number)
	method := "POST"
	if note != "" {
		method = "PUT"
		path += "/" + note
	}
	b, _ := json.Marshal(map[string]string{"body": body})
	var x struct{ ID int64 }
	if e = c.request(ctx, method, path, bytes.NewReader(b), &x); e != nil {
		return "", e
	}
	if x.ID <= 0 {
		return "", errors.New("ambiguous comment response")
	}
	return fmt.Sprint(x.ID), nil
}
