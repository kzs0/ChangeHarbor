// Package worker authenticates forge events and runs durable bounded jobs.
package worker

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/kzs0/reviewd/internal/jobs"
	"io"
	"net/http"
	"strings"
	"time"
)

type Options struct {
	GitHubSecret, GitLabSecret string
	MaxBytes                   int64
}
type Handler struct {
	q *jobs.Queue
	o Options
}

func NewHandler(q *jobs.Queue, o Options) *Handler {
	if o.MaxBytes <= 0 {
		o.MaxBytes = 1 << 20
	}
	return &Handler{q, o}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	provider := ""
	secret := ""
	switch r.URL.Path {
	case "/webhooks/github":
		provider = "github"
		secret = h.o.GitHubSecret
	case "/webhooks/gitlab":
		provider = "gitlab"
		secret = h.o.GitLabSecret
	default:
		http.NotFound(w, r)
		return
	}
	if secret == "" || h.q == nil {
		http.Error(w, "webhook not configured", 503)
		return
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, h.o.MaxBytes+1))
	if e != nil || int64(len(b)) > h.o.MaxBytes {
		http.Error(w, "body exceeds limit", 413)
		return
	}
	delivery := ""
	event := ""
	if provider == "github" {
		signature := strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
		got, e := hex.DecodeString(signature)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(b)
		if e != nil || !hmac.Equal(got, mac.Sum(nil)) {
			http.Error(w, "invalid signature", 401)
			return
		}
		delivery = r.Header.Get("X-GitHub-Delivery")
		event = r.Header.Get("X-GitHub-Event")
	} else {
		got := r.Header.Get("X-Gitlab-Token")
		if subtle.ConstantTimeCompare([]byte(secret), []byte(got)) != 1 {
			http.Error(w, "invalid token", 401)
			return
		}
		delivery = r.Header.Get("X-Gitlab-Event-UUID")
		event = r.Header.Get("X-Gitlab-Event")
	}
	if delivery == "" || len(delivery) > 256 || strings.ContainsAny(delivery, "\r\n\x00") {
		http.Error(w, "delivery identity required", 400)
		return
	}
	key := provider + ":" + delivery
	js, e := normalize(provider, event, key, b)
	if e != nil {
		http.Error(w, "invalid event", 400)
		return
	}
	if _, e = h.q.ReceiveAndEnqueue(r.Context(), key, b, js); e != nil {
		http.Error(w, "receipt could not be committed", 503)
		return
	}
	w.WriteHeader(202)
}

// CommandPrefilter reports whether a comment body might be a review
// command: its first non-blank line has exactly two fields, the second
// "review" and the first a mention (or, on GitHub, /changeharbor). It only
// saves jobs; the API copy of the comment is authoritative.
func CommandPrefilter(provider, body string) bool {
	for _, line := range strings.Split(body, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 2 || !strings.EqualFold(f[1], "review") {
			return false
		}
		if len(f[0]) > 1 && f[0][0] == '@' {
			return true
		}
		return provider == "github" && strings.EqualFold(f[0], "/changeharbor")
	}
	return false
}

func parseTime(v string) (time.Time, error) {
	t, e := time.Parse(time.RFC3339, v)
	if e != nil {
		t, e = time.Parse("2006-01-02 15:04:05 MST", v)
	}
	return t, e
}

func normalize(provider, event, key string, b []byte) ([]jobs.Job, error) {
	var x struct {
		Action      string
		Number      int
		Repository  struct{ ID int64 }
		PullRequest struct {
			Merged    bool
			UpdatedAt string `json:"updated_at"`
			Head      struct{ SHA string }
		} `json:"pull_request"`
		Changes struct {
			Base           json.RawMessage          `json:"base"`
			Draft          *struct{ Current *bool } `json:"draft"`
			WorkInProgress *struct{ Current *bool } `json:"work_in_progress"`
			TargetBranch   json.RawMessage          `json:"target_branch"`
		} `json:"changes"`
		Issue *struct {
			Number      int
			PullRequest json.RawMessage `json:"pull_request"`
		} `json:"issue"`
		Comment *struct {
			ID        int64
			Body      string
			CreatedAt string `json:"created_at"`
		} `json:"comment"`
		MergeRequest *struct{ IID int } `json:"merge_request"`
		Ref, After   string
		Project      struct{ ID int64 }
		Attributes   struct {
			ID            int64
			IID           int
			State, Action string
			Note          string
			NoteableType  string              `json:"noteable_type"`
			OldRev        string              `json:"oldrev"`
			UpdatedAt     string              `json:"updated_at"`
			CreatedAt     string              `json:"created_at"`
			LastCommit    struct{ ID string } `json:"last_commit"`
		} `json:"object_attributes"`
	}
	if e := json.Unmarshal(b, &x); e != nil {
		return nil, e
	}
	present := func(r json.RawMessage) bool { return len(r) > 0 && string(r) != "null" }
	ready := func(c *struct{ Current *bool }) bool { return c != nil && c.Current != nil && !*c.Current }
	kind := ""
	change := ""
	generation := 0
	if provider == "github" {
		switch event {
		case "pull_request":
			if x.Repository.ID <= 0 || x.Number <= 0 {
				return nil, fmt.Errorf("identity required")
			}
			change = fmt.Sprintf("github:%d:%d", x.Repository.ID, x.Number)
			switch {
			case x.Action == "closed" && x.PullRequest.Merged:
				kind = "ingest"
			case x.Action == "opened" || x.Action == "reopened" || x.Action == "synchronize" || x.Action == "ready_for_review":
				kind = "review"
			case x.Action == "edited" && present(x.Changes.Base):
				kind = "review"
			}
			if kind == "review" {
				t, e := time.Parse(time.RFC3339, x.PullRequest.UpdatedAt)
				if e != nil {
					return nil, e
				}
				generation = int(t.Unix())
			}
		case "issue_comment":
			if x.Action != "created" || x.Issue == nil || !present(x.Issue.PullRequest) || x.Comment == nil || !CommandPrefilter(provider, x.Comment.Body) {
				return nil, nil
			}
			if x.Repository.ID <= 0 || x.Issue.Number <= 0 || x.Comment.ID <= 0 {
				return nil, fmt.Errorf("identity required")
			}
			t, e := time.Parse(time.RFC3339, x.Comment.CreatedAt)
			if e != nil {
				return nil, e
			}
			change, kind, generation = fmt.Sprintf("github:%d:%d", x.Repository.ID, x.Issue.Number), "review", int(t.Unix())
		case "push":
			if x.Repository.ID <= 0 || x.Ref == "" || x.After == "" {
				return nil, fmt.Errorf("push identity required")
			}
			change = fmt.Sprintf("github:%d:%s", x.Repository.ID, x.Ref)
			kind = "ingest"
		}
	}
	if provider == "gitlab" {
		switch event {
		case "Merge Request Hook":
			if x.Project.ID <= 0 || x.Attributes.IID <= 0 {
				return nil, fmt.Errorf("identity required")
			}
			change = fmt.Sprintf("gitlab:%d:%d", x.Project.ID, x.Attributes.IID)
			if x.Attributes.State == "merged" || x.Attributes.Action == "merge" {
				kind = "ingest"
			} else if x.Attributes.State == "opened" {
				switch x.Attributes.Action {
				case "", "open", "reopen":
					kind = "review"
				case "update":
					// Only new commits, readiness or a retarget change what
					// would be reviewed or whether it is reviewed.
					if x.Attributes.OldRev != "" || ready(x.Changes.Draft) || ready(x.Changes.WorkInProgress) || present(x.Changes.TargetBranch) {
						kind = "review"
					}
				}
			}
			if kind == "review" {
				t, e := parseTime(x.Attributes.UpdatedAt)
				if e != nil {
					return nil, e
				}
				generation = int(t.Unix())
			}
		case "Note Hook":
			if x.Attributes.NoteableType != "MergeRequest" || (x.Attributes.Action != "" && x.Attributes.Action != "create") || !CommandPrefilter(provider, x.Attributes.Note) {
				return nil, nil
			}
			if x.Project.ID <= 0 || x.MergeRequest == nil || x.MergeRequest.IID <= 0 || x.Attributes.ID <= 0 {
				return nil, fmt.Errorf("identity required")
			}
			t, e := parseTime(x.Attributes.CreatedAt)
			if e != nil {
				return nil, e
			}
			change, kind, generation = fmt.Sprintf("gitlab:%d:%d", x.Project.ID, x.MergeRequest.IID), "review", int(t.Unix())
		case "Push Hook":
			if x.Project.ID <= 0 || x.Ref == "" || x.After == "" {
				return nil, fmt.Errorf("push identity required")
			}
			change = fmt.Sprintf("gitlab:%d:%s", x.Project.ID, x.Ref)
			kind = "ingest"
		}
	}
	if kind == "" {
		return nil, nil
	}
	return []jobs.Job{{ID: key, Key: key, ChangeID: change, Kind: kind, Generation: generation}}, nil
}
