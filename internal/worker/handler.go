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
		Ref, After string
		Project    struct{ ID int64 }
		Attributes struct {
			IID           int
			State, Action string
			UpdatedAt     string              `json:"updated_at"`
			LastCommit    struct{ ID string } `json:"last_commit"`
		} `json:"object_attributes"`
	}
	if e := json.Unmarshal(b, &x); e != nil {
		return nil, e
	}
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
			if x.Action == "closed" && x.PullRequest.Merged {
				kind = "ingest"
			} else if x.Action == "opened" || x.Action == "reopened" || x.Action == "synchronize" {
				kind = "review"
			}
			if kind == "review" {
				t, e := time.Parse(time.RFC3339, x.PullRequest.UpdatedAt)
				if e != nil {
					return nil, e
				}
				generation = int(t.Unix())
			}
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
				kind = "review"
			}
			if kind == "review" {
				t, e := time.Parse(time.RFC3339, x.Attributes.UpdatedAt)
				if e != nil {
					t, e = time.Parse("2006-01-02 15:04:05 MST", x.Attributes.UpdatedAt)
				}
				if e != nil {
					return nil, e
				}
				generation = int(t.Unix())
			}
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
