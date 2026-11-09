package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kzs0/reviewd/internal/jobs"
	"strings"
)

// Event contains only routing facts from an authenticated persisted receipt.
// Callers must map RepositoryID to host-controlled paths and resolve all source
// revisions; payload repository URLs/paths must never authorize filesystem reads.
type Event struct {
	Provider, RepositoryID, Head, Target, Branch, Kind string
	ChangeNumber                                       int
	// CommentID is set for review command jobs: the comment (GitHub) or
	// note (GitLab) to read from the API. Head, Target and Branch are then
	// empty; the change's facts come from the API.
	CommentID string
}

func EventFromJob(j jobs.Job, b []byte) (Event, error) {
	provider, _, ok := strings.Cut(j.ID, ":")
	if !ok || (provider != "github" && provider != "gitlab") {
		return Event{}, errors.New("unsupported job provider")
	}
	var x struct {
		Number      int
		Ref, After  string
		Repository  struct{ ID int64 }
		PullRequest struct {
			Head           struct{ SHA string }
			Base           struct{ SHA, Ref string }
			MergeCommitSHA string `json:"merge_commit_sha"`
		} `json:"pull_request"`
		Issue *struct {
			Number int
		} `json:"issue"`
		Comment *struct {
			ID int64
		} `json:"comment"`
		ObjectKind   string             `json:"object_kind"`
		MergeRequest *struct{ IID int } `json:"merge_request"`
		Project      struct{ ID int64 }
		Attributes   struct {
			ID             int64
			NoteableType   string `json:"noteable_type"`
			IID            int
			TargetBranch   string              `json:"target_branch"`
			LastCommit     struct{ ID string } `json:"last_commit"`
			MergeCommitSHA string              `json:"merge_commit_sha"`
		} `json:"object_attributes"`
	}
	if e := json.Unmarshal(b, &x); e != nil {
		return Event{}, e
	}
	e := Event{Provider: provider, Kind: j.Kind}
	if j.Kind == "review" && provider == "github" && x.Comment != nil {
		if x.Repository.ID <= 0 || x.Issue == nil || x.Issue.Number <= 0 || x.Comment.ID <= 0 {
			return Event{}, errors.New("receipt has insufficient comment routing facts")
		}
		e.RepositoryID, e.ChangeNumber, e.CommentID = fmt.Sprint(x.Repository.ID), x.Issue.Number, fmt.Sprint(x.Comment.ID)
		return e, nil
	}
	if j.Kind == "review" && provider == "gitlab" && (x.ObjectKind == "note" || x.Attributes.NoteableType != "") {
		if x.Project.ID <= 0 || x.MergeRequest == nil || x.MergeRequest.IID <= 0 || x.Attributes.ID <= 0 {
			return Event{}, errors.New("receipt has insufficient comment routing facts")
		}
		e.RepositoryID, e.ChangeNumber, e.CommentID = fmt.Sprint(x.Project.ID), x.MergeRequest.IID, fmt.Sprint(x.Attributes.ID)
		return e, nil
	}
	if provider == "github" {
		e.RepositoryID = fmt.Sprint(x.Repository.ID)
		e.ChangeNumber = x.Number
		e.Head = x.PullRequest.Head.SHA
		e.Target = x.PullRequest.Base.SHA
		e.Branch = x.PullRequest.Base.Ref
		if j.Kind == "ingest" {
			e.Head = x.PullRequest.MergeCommitSHA
		}
	} else {
		e.RepositoryID = fmt.Sprint(x.Project.ID)
		e.ChangeNumber = x.Attributes.IID
		e.Head = x.Attributes.LastCommit.ID
		e.Branch = x.Attributes.TargetBranch
		if j.Kind == "ingest" {
			e.Head = x.Attributes.MergeCommitSHA
		}
	}
	if x.Ref != "" {
		e.Branch = strings.TrimPrefix(x.Ref, "refs/heads/")
		e.Head = x.After
	}
	if e.RepositoryID == "0" || e.Head == "" || e.Branch == "" {
		return Event{}, errors.New("receipt has insufficient immutable routing facts")
	}
	return e, nil
}
