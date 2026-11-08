package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/reviewd/internal/archive"
	"github.com/kzs0/reviewd/internal/contextbuild"
	"github.com/kzs0/reviewd/internal/contextpack"
	"github.com/kzs0/reviewd/internal/evaluate"
	gitreader "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/jobs"
	"github.com/kzs0/reviewd/internal/journal"
	"github.com/kzs0/reviewd/internal/mergeingest"
	"github.com/kzs0/reviewd/internal/model"
	riskpolicy "github.com/kzs0/reviewd/internal/policy"
	"github.com/kzs0/reviewd/internal/release"
	"github.com/kzs0/reviewd/internal/review"
	"github.com/kzs0/reviewd/internal/statelock"
	"github.com/kzs0/reviewd/internal/storage"
	"github.com/kzs0/reviewd/internal/worker"
)

func strictJSON(b []byte, v any) error {
	if len(b) > 1<<20 {
		return errors.New("JSON exceeds 1 MiB")
	}
	if e := uniqueCommandJSON(b); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func saveRun(ctx context.Context, root, kind string, v any) (string, error) {
	s, e := storage.Open(root)
	if e != nil {
		return "", e
	}
	defer s.Close()
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	digest, e := s.Put(ctx, b)
	if e != nil {
		return "", e
	}
	if e = s.Save(ctx, "run", digest, b); e != nil {
		return "", e
	}
	if e = s.Save(ctx, "run_kind", digest, []byte(kind)); e != nil {
		return "", e
	}
	return digest, nil
}
func saveAndAnnounce(ctx context.Context, root, kind string, v any) error {
	id, e := saveRun(ctx, root, kind, v)
	if e == nil {
		fmt.Fprintln(os.Stderr, "saved run:", id)
	}
	return e
}
func loadRun(ctx context.Context, root, id string, v any) error {
	if b, e := os.ReadFile(id); e == nil {
		return strictJSON(b, v)
	}
	s, e := storage.Open(root)
	if e != nil {
		return e
	}
	defer s.Close()
	b, e := s.Load(ctx, "run", id)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func modelFor(ctx context.Context, configPath, responsePath, state, runID string) (model.Model, func(), error) {
	if (configPath == "") == (responsePath == "") {
		return nil, nil, errors.New("provide exactly one of --response and --model-config")
	}
	if configPath == "" {
		b, e := os.ReadFile(responsePath)
		return &model.Fake{Response: model.Response{Raw: b}}, func() {}, e
	}
	f, e := os.Open(configPath)
	if e != nil {
		return nil, nil, e
	}
	defer f.Close()
	c, e := model.DecodeHTTPConfig(f)
	if e != nil {
		return nil, nil, e
	}
	m, e := model.NewHTTP(c)
	if e != nil {
		return nil, nil, e
	}
	s, e := storage.Open(state)
	if e != nil {
		return nil, nil, e
	}
	m.Audit = s
	m.RunID = runID
	return m, func() { s.Close() }, ctx.Err()
}
func identityID(v any) string {
	b, _ := json.Marshal(v)
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}
func extraCommand(ctx context.Context, args []string) (int, error) {
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	state := f.String("state", ".reviewd", "durable state directory")
	switch args[0] {
	case "backup":
		output := f.String("output", "", "destination archive path")
		if f.Parse(args[1:]) != nil || *output == "" {
			return 2, nil
		}
		return 0, archive.Backup(ctx, *state, *output)
	case "restore":
		input := f.String("input", "", "source archive path")
		if f.Parse(args[1:]) != nil || *input == "" {
			return 2, nil
		}
		return 0, archive.Restore(ctx, *input, *state)
	case "context":
		if len(args) < 2 || args[1] != "build" {
			return 2, nil
		}
		repo := f.String("repo", ".", "local Git repository")
		ref := f.String("ref", "HEAD", "revision")
		if f.Parse(args[2:]) != nil {
			return 2, nil
		}
		s, e := storage.Open(*state)
		if e != nil {
			return 1, e
		}
		defer s.Close()
		snap, e := contextbuild.Build(ctx, s, contextbuild.BuildOptions{Path: *repo, Repository: filepath.Clean(*repo), Ref: *ref, MaxFiles: 10000, MaxBytes: 8 << 20})
		if e != nil {
			return 1, e
		}
		if e = s.Save(ctx, "snapshot_active", filepath.Clean(*repo), []byte(snap.ID)); e != nil {
			return 1, e
		}
		return 0, emit(snap)
	case "journal":
		if len(args) < 2 || args[1] != "reconcile" {
			return 2, nil
		}
		repo := f.String("repo", ".", "local Git repository")
		branch := f.String("branch", "main", "tracked branch identity")
		through := f.String("through", "HEAD", "immutable endpoint or ref")
		reset := f.Bool("reset", false, "explicit new epoch on divergent history")
		if f.Parse(args[2:]) != nil {
			return 2, nil
		}
		s, e := storage.Open(*state)
		if e != nil {
			return 1, e
		}
		defer s.Close()
		l, e := journal.Open(ctx, s)
		if e != nil {
			return 1, e
		}
		w, e := mergeingest.ReconcileGit(ctx, l, s, mergeingest.GitOptions{Path: *repo, Repository: filepath.Clean(*repo), Branch: *branch, Through: *through, Reset: *reset, MaxBytes: 8 << 20})
		if e != nil {
			return 1, e
		}
		return 0, emit(w)
	case "explain":
		id := f.String("run", "", "durable ID or saved JSON file")
		if f.Parse(args[1:]) != nil || *id == "" {
			return 2, nil
		}
		var v any
		if e := loadRun(ctx, *state, *id, &v); e != nil {
			return 1, e
		}
		return 0, emit(v)
	case "evaluate":
		fixtures := f.String("fixtures", "", "fixture manifest JSON")
		if f.Parse(args[1:]) != nil || *fixtures == "" {
			return 2, nil
		}
		r, e := evaluate.Evaluate(ctx, *fixtures)
		if e != nil {
			return 1, e
		}
		if e = emit(r); e != nil {
			return 1, e
		}
		if !r.Passed {
			return 3, nil
		}
		return 0, nil
	case "serve":
		config := f.String("config", "", "strict service config JSON")
		if f.Parse(args[1:]) != nil || *config == "" {
			return 2, nil
		}
		c, e := loadServerConfig(*config)
		if e != nil {
			return 2, e
		}
		if e := c.validate(); e != nil {
			return 2, e
		}
		return serve(ctx, c)
	}
	return 2, nil
}

type repositoryConfig struct {
	PolicyFile         string                      `json:"policy_file"`
	SummaryModelConfig string                      `json:"summary_model_config"`
	SummaryLimits      *contextbuild.SummaryLimits `json:"summary_limits"`
	Path               string                      `json:"path"`
	Branch             string                      `json:"branch"`
	ModelConfig        string                      `json:"model_config"`
	Response           string                      `json:"response"`
	// ForgeConfig enables the full forge flow for MR/PR events: API-captured
	// review, then comment and approval per that configuration's modes.
	ForgeConfig string `json:"forge_config"`

	forge *forgeConfig // compiled from reviewd.json
}
type serverConfig struct {
	ContextRebuildInterval string                      `json:"context_rebuild_interval"`
	ReconciliationInterval string                      `json:"reconciliation_interval"`
	Repositories           map[string]repositoryConfig `json:"repositories"`
	Address                string                      `json:"address"`
	State                  string                      `json:"state"`
	GitHubSecretEnv        string                      `json:"github_secret_env"`
	GitLabSecretEnv        string                      `json:"gitlab_secret_env"`
}

func (c serverConfig) validate() error {
	if _, e := rebuildInterval(c); e != nil {
		return e
	}
	if _, e := reconciliationInterval(c); e != nil {
		return e
	}
	for key, repo := range c.Repositories {
		if (repo.SummaryModelConfig == "") != (repo.SummaryLimits == nil) {
			return errors.New("background summary model and separate limits must be configured together")
		}
		if key == "" || repo.Path == "" || repo.Branch == "" {
			return errors.New("invalid trusted repository mapping")
		}
	}
	host, _, e := net.SplitHostPort(c.Address)
	if e != nil {
		return e
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("service address must be an explicit loopback IP")
	}
	if c.State == "" {
		return errors.New("state is required")
	}
	if c.GitHubSecretEnv == "" && c.GitLabSecretEnv == "" {
		return errors.New("at least one webhook secret environment variable required")
	}
	for _, n := range []string{c.GitHubSecretEnv, c.GitLabSecretEnv} {
		if n != "" && (strings.ContainsAny(n, "=\x00") || os.Getenv(n) == "") {
			return errors.New("configured webhook secret environment variable is missing")
		}
	}
	return nil
}
func serve(ctx context.Context, c serverConfig) (int, error) {
	source, ctx := bedrock.Source(ctx, "reviewd.worker")
	defer source.Done()
	if e := os.MkdirAll(c.State, 0700); e != nil {
		return 1, e
	}
	release, e := statelock.Acquire(c.State)
	if e != nil {
		return 1, e
	}
	defer release()
	q, e := jobs.Open(filepath.Join(c.State, "jobs.db"))
	if e != nil {
		return 1, e
	}
	defer q.Close()
	workCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	workerDone := make(chan error, 1)
	workerFinished := make(chan struct{})
	go func() {
		defer close(workerFinished)
		workerDone <- worker.Run(workCtx, q, worker.RunOptions{WorkerID: "reviewd-local", PollInterval: time.Second, Lease: 30 * time.Second, JobTimeout: 5 * time.Minute, MaxAttempts: 2}, func(ctx context.Context, j jobs.Job, b []byte) error { return processEvent(ctx, c, j, b) })
	}()
	defer func() {
		cancelWorker()
		select {
		case <-workerFinished:
		case <-time.After(5 * time.Second):
		}
	}()
	interval, e := reconciliationInterval(c)
	if e != nil {
		return 2, e
	}
	reconcileFinished := make(chan struct{})
	go func() {
		defer close(reconcileFinished)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			if err := reconcileRepositories(workCtx, c); err != nil && workCtx.Err() == nil {
				fmt.Fprintln(os.Stderr, "tracked-branch reconciliation incomplete; will retry")
			}
			if err := rebuildRepositories(workCtx, c, time.Now()); err != nil && workCtx.Err() == nil {
				fmt.Fprintln(os.Stderr, "scheduled context build incomplete; inspect audit records")
			}
			select {
			case <-workCtx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	defer func() {
		cancelWorker()
		select {
		case <-reconcileFinished:
		case <-time.After(5 * time.Second):
		}
	}()
	handler := worker.NewHandler(q, worker.Options{GitHubSecret: os.Getenv(c.GitHubSecretEnv), GitLabSecret: os.Getenv(c.GitLabSecretEnv), MaxBytes: 1 << 20})
	mux := http.NewServeMux()
	mux.Handle("/webhooks/", handler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok","mode":"shadow","processing":"durable_shadow_worker"}`)
	})
	server := &http.Server{Addr: c.Address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case e := <-workerDone:
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(stop)
		if ctx.Err() != nil && errors.Is(e, ctx.Err()) {
			return 0, nil
		}
		return 1, e
	case e := <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return 0, nil
		}
		return 1, e
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return 0, server.Shutdown(stop)
	}
}

// localReleaseInput resolves refs once, reconciles actual landed transitions and
// tests membership by immutable ancestry, never timestamps or branch labels.
func localReleaseInput(ctx context.Context, repo, baseline, candidate, branch, state string) (release.Input, error) {
	r := gitreader.Reader{Repo: repo, MaxBytes: 8 << 20}
	base, e := r.Resolve(ctx, baseline)
	if e != nil {
		return release.Input{}, e
	}
	head, e := r.Resolve(ctx, candidate)
	if e != nil {
		return release.Input{}, e
	}
	in := release.Input{Identity: release.Identity{Repository: filepath.Clean(repo), BaselineRef: baseline, BaselineOID: base, CandidateRef: candidate, CandidateOID: head}, MaxRequestBytes: 8 << 20}
	ancestry, e := mergeingest.Ancestry(ctx, repo, head, 8<<20)
	if e != nil {
		return in, e
	}
	member := map[string]bool{}
	for _, id := range ancestry {
		member[id] = true
	}
	in.BaselineAncestor = member[base]
	if !in.BaselineAncestor {
		in.Unknowns = []string{"baseline is not an ancestor of candidate"}
		return in, nil
	}
	d, e := r.Diff(ctx, base, head)
	if e != nil {
		return in, e
	}
	in.NetDiff = d.Patch
	in.ChangedPaths = make([]release.ChangedPath, 0, len(d.Files))
	for _, file := range d.Files {
		in.ChangedPaths = append(in.ChangedPaths, release.ChangedPath{OldPath: file.OldPath, NewPath: file.NewPath})
	}
	in.Evidence = append(in.Evidence, release.Evidence{ID: "net_diff", Kind: "source", State: "present", CandidateOID: head, Required: true, Content: "Exact source change supplied in NetDiff"})
	for _, anchor := range d.Anchors {
		in.SourceAnchors = append(in.SourceAnchors, release.SourceAnchor{EvidenceID: "net_diff", Path: anchor.Path, Line: anchor.Line})
	}
	in.SourceComplete = true
	s, e := storage.Open(state)
	if e != nil {
		return in, e
	}
	defer s.Close()
	l, e := journal.Open(ctx, s)
	if e != nil {
		return in, e
	}
	_, e = mergeingest.ReconcileGit(ctx, l, s, mergeingest.GitOptions{Path: repo, Repository: in.Identity.Repository, Branch: branch, Through: head, MaxBytes: 8 << 20})
	if e != nil {
		in.Unknowns = []string{"journal reconciliation failed: " + e.Error()}
		return in, nil
	}
	all, e := l.Entries(ctx, in.Identity.Repository, branch)
	if e != nil {
		return in, e
	}
	old, e := mergeingest.Ancestry(ctx, repo, base, 8<<20)
	if e != nil {
		return in, e
	}
	prior := map[string]bool{}
	for _, id := range old {
		prior[id] = true
	}
	selected := []journal.Transition{}
	for _, t := range all {
		if member[t.To] && !prior[t.To] {
			selected = append(selected, t)
		}
	}
	b, e := json.Marshal(selected)
	if e != nil {
		return in, e
	}
	in.Inventory = string(b)
	in.Evidence = append(in.Evidence, release.Evidence{ID: "release_inventory", Kind: "journal", State: "present", CandidateOID: head, Required: true, Content: "Exact verified landed inventory supplied in Inventory"})
	in.JournalComplete = true
	return in, nil
}

func reviewContext(ctx context.Context, state, repo, head, patch string) ([]contextpack.Item, error) {
	items := []contextpack.Item{{ID: "diff", Layer: "diff", Content: patch, Required: true}}
	s, e := storage.Open(state)
	if e != nil {
		return nil, e
	}
	defer s.Close()
	id, e := s.Load(ctx, "snapshot_active", filepath.Clean(repo))
	if e != nil {
		return items, nil
	}
	snap, e := contextbuild.Load(ctx, s, string(id))
	if e != nil {
		return nil, e
	}
	l, e := journal.Open(ctx, s)
	if e != nil {
		return nil, e
	}
	m, e := contextbuild.Materialize(ctx, s, l, contextbuild.MaterializeOptions{Path: repo, Repository: filepath.Clean(repo), Branch: "main", Ref: head, Snapshots: []contextbuild.Snapshot{snap}})
	if e != nil {
		return nil, e
	}
	// Immutable summaries are optional; exact current diff remains required.
	b, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	items = append(items, contextpack.Item{ID: "context", Layer: "context", Content: string(b)})
	return items, nil
}

func processEvent(ctx context.Context, c serverConfig, j jobs.Job, b []byte) error {
	event, e := worker.EventFromJob(j, b)
	if e != nil {
		return e
	}
	o, ok := c.Repositories[event.Provider+":"+event.RepositoryID]
	if !ok || o.Path == "" || o.Branch == "" {
		return errors.New("repository has no trusted local mapping")
	}
	if event.Branch != o.Branch {
		return errors.New("event targets an unconfigured branch")
	}
	forge := o.forge
	if forge == nil && o.ForgeConfig != "" {
		fc, e := loadForgeConfig(o.ForgeConfig)
		if e != nil {
			return e
		}
		if fc.Provider != event.Provider || fc.RepositoryID != event.RepositoryID || fc.RepoPath != o.Path || fc.Branch != o.Branch {
			return errors.New("forge configuration does not match the trusted repository mapping")
		}
		forge = &fc
	}
	if event.Kind == "ingest" {
		if e := ingestEvent(ctx, c, o, event); e != nil {
			return e
		}
		if forge != nil && forge.Provider == "gitlab" && forge.BotUserID != "" {
			s, e := storage.Open(c.State)
			if e != nil {
				return e
			}
			defer s.Close()
			_, e = revalidateRun(ctx, *forge, s)
			return e
		}
		return nil
	}
	if event.Kind == "review" && forge != nil {
		return automateChange(ctx, c, o, *forge, event.ChangeNumber)
	}
	if event.Kind != "review" {
		return errors.New("unsupported durable job kind")
	}
	target := event.Target
	if target == "" {
		target = o.Branch
	}
	d, e := (gitreader.Reader{Repo: o.Path, MaxBytes: 8 << 20}).Diff(ctx, target, event.Head)
	if e != nil {
		return e
	}
	m, close, e := modelFor(ctx, o.ModelConfig, o.Response, c.State, identityID(struct{ Job, Head, Target string }{j.ID, d.Head, d.Base}))
	if e != nil {
		return e
	}
	defer close()
	items, e := reviewContext(ctx, c.State, o.Path, d.Head, d.Patch)
	if e != nil {
		return e
	}
	minimum, threshold := 1, 3
	var floor *riskpolicy.Decision
	if o.PolicyFile != "" {
		decision, err := evaluateDiffPolicy(ctx, o.PolicyFile, d)
		if err != nil {
			return err
		}
		floor = &decision
		minimum = decision.EnforcedMinimum
		threshold = decision.Threshold
	}
	hostItem, rubric, e := hostPolicyItem(minimum, floor, d, o.PolicyFile)
	if e != nil {
		return e
	}
	items = append(items, hostItem)
	identity := identityID(struct{ Repository, Head, Target, Rubric string }{filepath.Clean(o.Path), d.Head, d.Base, rubric})
	r, e := (review.Engine{Model: identityModel{m, identity}}).Run(ctx, review.Input{Items: items, Limits: contextpack.Limits{MaxBytes: 1 << 20, MaxTokens: (1 << 20) + 4096, ReserveTokens: 4096}, EvidenceIDs: []string{"diff", "host_policy"}, Minimum: minimum, Threshold: threshold, RequirementsPass: false, Anchors: d.Anchors})
	if e != nil {
		return e
	}
	return saveAndAnnounce(ctx, c.State, "review", r)
}

func ingestEvent(ctx context.Context, c serverConfig, o repositoryConfig, event worker.Event) error {
	s, e := storage.Open(c.State)
	if e != nil {
		return e
	}
	defer s.Close()
	l, e := journal.Open(ctx, s)
	if e != nil {
		return e
	}
	_, e = mergeingest.ReconcileGit(ctx, l, s, mergeingest.GitOptions{Path: o.Path, Repository: filepath.Clean(o.Path), Branch: o.Branch, Through: event.Head, MaxBytes: 8 << 20})
	return e
}

// automateChange runs the forge flow for one change under the service's
// state lock: review from API-captured revisions (the webhook only names the
// change), then comment and publish as the forge configuration allows.
func automateChange(ctx context.Context, c serverConfig, o repositoryConfig, fc forgeConfig, number int) error {
	if number <= 0 {
		return errors.New("event has no change number")
	}
	id, result, _, e := forgeReview(ctx, fc, changeURL(fc, number), reviewOptions{State: c.State, Response: o.Response, ModelConfig: o.ModelConfig, Minimum: 1, Threshold: 3, Cap: 1 << 20})
	if e != nil {
		return e
	}
	fmt.Fprintln(os.Stderr, "saved forge review:", id)
	if result.Status != "completed" {
		return nil
	}
	s, e := storage.Open(c.State)
	if e != nil {
		return e
	}
	defer s.Close()
	if fc.CommentMode == "post" {
		if _, _, e = commentRun(ctx, fc, s, id); e != nil {
			return e
		}
	}
	if fc.Mode == "submit" {
		if _, _, e = publishRun(ctx, fc, s, id); e != nil {
			return e
		}
	}
	return nil
}
