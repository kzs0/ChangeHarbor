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
		Project    struct{ ID int64 }
		Attributes struct {
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
