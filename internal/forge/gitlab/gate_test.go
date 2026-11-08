package gitlab

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const (
	head   = "abc"
	target = "456"
	merged = "aaa"
)

func goodExpect() Expect {
	return Expect{ProjectID: "9", ProjectPath: "group/app", IID: 7, Head: head, Target: target, MergeBase: "def", TargetBranch: "main", BotUserID: "5", RequiredJobs: map[string]string{"test": "project:9"},
		CITrust: "external_config", ChangedPaths: []string{"src/a.go"}}
}
func goodFacts() Facts {
	return Facts{
		Change:                Change{RepositoryID: "9", SourceRepositoryID: "9", HeadOID: head, TargetOID: target, MergeBaseOID: "def", TargetBranch: "main", State: "opened", HeadPipelineID: 500},
		UserID:                "5",
		Project:               ProjectSettings{PipelineMustSucceed: true, MergeMethod: "ff", CIConfigPath: ".gitlab-ci.yml@group/ci-config"},
		ApprovalSettings:      ApprovalSettings{ResetOnPush: true},
		ApprovalSettingsKnown: true,
		Branch:                ProtectedBranch{Protected: true},
		Rules:                 []ApprovalRule{{Name: "Maintainers", Type: "regular", Required: 1, EligibleIDs: []string{"5", "6"}}},
		RulesKnown:            true,
		Pipeline:              &Pipeline{ID: 500, ProjectID: "9", SHA: head, Ref: "refs/merge-requests/7/head", Status: "success", Source: "merge_request_event"},
		Jobs:                  []Job{{Name: "test", Status: "success", PipelineID: 500}, {Name: "lint", Status: "success", PipelineID: 500}},
	}
}

func TestEvaluateAcceptsFullyEstablishedRequirements(t *testing.T) {
	passed, reason := Evaluate(goodFacts(), goodExpect())
	if reason != "" || !passed["test"] || len(passed) != 1 {
		t.Fatalf("%q %v", reason, passed)
	}
}

func TestEvaluateAcceptsSupportedVariants(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Facts, *Expect)
	}{
		{"semi-linear merge method", func(f *Facts, _ *Expect) { f.Project.MergeMethod = "rebase_merge" }},
		{"merge commit with merged results pipelines", func(f *Facts, _ *Expect) {
			f.Project.MergeMethod = "merge"
			f.Project.MergedResultsPipelines = true
		}},
		{"merged results pipeline bound to target and head", func(f *Facts, _ *Expect) {
			f.Project.MergedResultsPipelines = true
			f.Pipeline.SHA, f.Pipeline.Ref, f.PipelineParents = merged, "refs/merge-requests/7/merge", []string{target, head}
		}},
		{"branch push pipeline in target project", func(f *Facts, _ *Expect) { f.Pipeline.Source, f.Pipeline.Ref = "push", "feature" }},
		{"rule already approved by humans", func(f *Facts, _ *Expect) {
			f.Rules = []ApprovalRule{{Name: "Security", Type: "regular", Required: 2, Approved: true, EligibleIDs: []string{"7", "8"}, ApprovedByIDs: []string{"7", "8"}}}
		}},
		{"any approver rule", func(f *Facts, _ *Expect) {
			f.Rules = []ApprovalRule{{Name: "All Members", Type: "any_approver", Required: 1}}
		}},
		{"bot completes a rule with one human already", func(f *Facts, _ *Expect) {
			f.Rules = []ApprovalRule{{Name: "Backend", Type: "regular", Required: 2, EligibleIDs: []string{"5", "6"}, ApprovedByIDs: []string{"6"}}}
		}},
		{"optional rule", func(f *Facts, _ *Expect) {
			f.Rules = []ApprovalRule{{Name: "FYI", Type: "regular", Required: 0, EligibleIDs: []string{"9"}}}
		}},
		{"no rules", func(f *Facts, _ *Expect) { f.Rules = nil }},
		{"no required jobs", func(_ *Facts, e *Expect) { e.RequiredJobs = nil }},
		{"successful allow_failure job", func(f *Facts, _ *Expect) { f.Jobs[0].AllowFailure = true }},
		{"mapped fork source", func(f *Facts, _ *Expect) { f.Change.SourceRepositoryID = "12" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, e := goodFacts(), goodExpect()
			tc.mut(&f, &e)
			if _, reason := Evaluate(f, e); reason != "" {
				t.Fatalf("withheld: %s", reason)
			}
		})
	}
}

func TestEvaluateWithholds(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mut        func(*Facts, *Expect)
	}{
		{"head moved", "HEAD or target", func(f *Facts, _ *Expect) { f.Change.HeadOID = "fff" }},
		{"target moved", "HEAD or target", func(f *Facts, _ *Expect) { f.Change.TargetOID = "fff" }},
		{"merged", "not open", func(f *Facts, _ *Expect) { f.Change.State = "merged" }},
		{"closed", "not open", func(f *Facts, _ *Expect) { f.Change.State = "closed" }},
		{"locked", "not open", func(f *Facts, _ *Expect) { f.Change.State = "locked" }},
		{"draft", "draft", func(f *Facts, _ *Expect) { f.Change.Draft = true }},
		{"retargeted", "different branch", func(f *Facts, _ *Expect) { f.Change.TargetBranch = "other" }},
		{"stale GitLab diff", "diff", func(f *Facts, _ *Expect) { f.Change.MergeBaseOID = "old" }},
		{"token identity", "bot user", func(f *Facts, _ *Expect) { f.UserID = "6" }},
		{"pipelines optional", "pipelines are not required", func(f *Facts, _ *Expect) { f.Project.PipelineMustSucceed = false }},
		{"skipped pipelines mergeable", "skipped", func(f *Facts, _ *Expect) { f.Project.AllowSkippedPipeline = true }},
		{"merge commit without merged results", "freshness", func(f *Facts, _ *Expect) { f.Project.MergeMethod = "merge" }},
		{"unknown merge method", "freshness", func(f *Facts, _ *Expect) { f.Project.MergeMethod = "" }},
		{"approval settings unavailable", "Premium", func(f *Facts, _ *Expect) { f.ApprovalSettingsKnown = false }},
		{"approvals survive push", "reset on push", func(f *Facts, _ *Expect) { f.ApprovalSettings.ResetOnPush = false }},
		{"selective code owner removal", "reset on push", func(f *Facts, _ *Expect) { f.ApprovalSettings.SelectiveCodeOwnerRemovals = true }},
		{"unprotected target", "protected", func(f *Facts, _ *Expect) { f.Branch.Protected = false }},
		{"force push allowed", "force push", func(f *Facts, _ *Expect) { f.Branch.AllowForcePush = true }},
		{"approval rules unavailable", "Premium", func(f *Facts, _ *Expect) { f.RulesKnown = false }},
		{"two humans still required", "human approval", func(f *Facts, _ *Expect) { f.Rules[0].Required = 2 }},
		{"bot not eligible", "human approval", func(f *Facts, _ *Expect) { f.Rules[0].EligibleIDs = []string{"6"} }},
		{"code owner rule without bot", "human approval", func(f *Facts, _ *Expect) {
			f.Rules = append(f.Rules, ApprovalRule{Name: "CODEOWNERS", Type: "code_owner", Required: 1, EligibleIDs: []string{"8"}})
		}},
		{"no head pipeline", "no head pipeline", func(f *Facts, _ *Expect) { f.Change.HeadPipelineID = 0; f.Pipeline = nil }},
		{"pipeline is not head pipeline", "not bound", func(f *Facts, _ *Expect) { f.Pipeline.ID = 499 }},
		{"fork project pipeline", "not trusted", func(f *Facts, _ *Expect) { f.Pipeline.ProjectID = "12" }},
		{"api triggered pipeline", "not trusted", func(f *Facts, _ *Expect) { f.Pipeline.Source = "api" }},
		{"trigger token pipeline", "not trusted", func(f *Facts, _ *Expect) { f.Pipeline.Source = "trigger" }},
		{"manual web pipeline", "not trusted", func(f *Facts, _ *Expect) { f.Pipeline.Source = "web" }},
		{"pipeline running", "not succeeded", func(f *Facts, _ *Expect) { f.Pipeline.Status = "running" }},
		{"pipeline failed", "not succeeded", func(f *Facts, _ *Expect) { f.Pipeline.Status = "failed" }},
		{"pipeline on other revision", "not bound", func(f *Facts, _ *Expect) { f.Pipeline.SHA = "fff" }},
		{"merged results built on stale target", "not bound", func(f *Facts, _ *Expect) {
			f.Pipeline.SHA, f.Pipeline.Ref, f.PipelineParents = merged, "refs/merge-requests/7/merge", []string{"oldtarget", head}
		}},
		{"merged results parents swapped", "not bound", func(f *Facts, _ *Expect) {
			f.Pipeline.SHA, f.Pipeline.Ref, f.PipelineParents = merged, "refs/merge-requests/7/merge", []string{head, target}
		}},
		{"merged results for another MR", "not bound", func(f *Facts, _ *Expect) {
			f.Pipeline.SHA, f.Pipeline.Ref, f.PipelineParents = merged, "refs/merge-requests/8/merge", []string{target, head}
		}},
		{"merge train pipeline", "not bound", func(f *Facts, _ *Expect) {
			f.Pipeline.SHA, f.Pipeline.Ref, f.PipelineParents = merged, "refs/merge-requests/7/train", []string{"prior", head}
		}},
		{"required job missing", "missing", func(f *Facts, _ *Expect) { f.Jobs = f.Jobs[1:] }},
		{"required job failed", "failed", func(f *Facts, _ *Expect) { f.Jobs[0].Status = "failed" }},
		{"allowed failure is still a failure", "failed", func(f *Facts, _ *Expect) { f.Jobs[0].Status, f.Jobs[0].AllowFailure = "failed", true }},
		{"required job skipped", "failed", func(f *Facts, _ *Expect) { f.Jobs[0].Status = "skipped" }},
		{"required job manual", "failed", func(f *Facts, _ *Expect) { f.Jobs[0].Status = "manual" }},
		{"duplicate job name", "ambiguous", func(f *Facts, _ *Expect) {
			f.Jobs = append(f.Jobs, Job{Name: "test", Status: "success", PipelineID: 500})
		}},
		{"job from another pipeline", "missing", func(f *Facts, _ *Expect) { f.Jobs[0].PipelineID = 499 }},
		{"untrusted producer mapping", "producer", func(_ *Facts, e *Expect) { e.RequiredJobs["test"] = "project:12" }},
		{"empty producer", "producer", func(_ *Facts, e *Expect) { e.RequiredJobs["test"] = "" }},
		{"missing bot identity", "bot user", func(_ *Facts, e *Expect) { e.BotUserID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, e := goodFacts(), goodExpect()
			tc.mut(&f, &e)
			passed, reason := Evaluate(f, e)
			if reason == "" || !strings.Contains(reason, tc.want) {
				t.Fatalf("reason %q want %q", reason, tc.want)
			}
			if passed != nil {
				t.Fatalf("withheld evaluation leaked passed checks %v", passed)
			}
		})
	}
}

func collectRoutes(over map[string]func(http.ResponseWriter, *http.Request)) map[string]func(http.ResponseWriter, *http.Request) {
	r := map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/merge_requests/7":         body(mrJSON),
		"GET /projects/9/repository/branches/main": body(`{"name":"main","commit":{"id":"456"}}`),
		"GET /user":                                       body(`{"id":5}`),
		"GET /projects/9":                                 body(`{"id":9,"only_allow_merge_if_pipeline_succeeds":true,"merge_method":"ff","ci_config_path":".gitlab-ci.yml@group/ci-config"}`),
		"GET /projects/9/approvals":                       body(`{"reset_approvals_on_push":true}`),
		"GET /projects/9/protected_branches/main":         body(`{"name":"main","allow_force_push":false}`),
		"GET /projects/9/merge_requests/7/approval_state": body(`{"rules":[{"name":"M","rule_type":"regular","approvals_required":1,"approved":false,"eligible_approvers":[{"id":5}],"approved_by":[]}]}`),
		"GET /projects/9/pipelines/500":                   body(`{"id":500,"project_id":9,"sha":"abc","ref":"refs/merge-requests/7/head","status":"success","source":"merge_request_event"}`),
		"GET /projects/9/pipelines/500/jobs":              body(`[{"id":1,"name":"test","status":"success","pipeline":{"id":500,"project_id":9}}]`),
	}
	for k, v := range over {
		if v == nil {
			delete(r, k)
		} else {
			r[k] = v
		}
	}
	return r
}

func TestCollectGathersLiveFacts(t *testing.T) {
	s := serve(t, collectRoutes(nil))
	f, e := NewClient(s.URL, "", s.Client(), 1<<20).Collect(context.Background(), goodExpect())
	if e != nil {
		t.Fatal(e)
	}
	if _, reason := Evaluate(f, goodExpect()); reason != "" {
		t.Fatalf("live facts withheld: %s %+v", reason, f)
	}
}

func TestCollectTreatsPremiumEndpointsAsUnknownNotErrors(t *testing.T) {
	s := serve(t, collectRoutes(map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/approvals":                       status(403, `{"message":"403 Forbidden"}`),
		"GET /projects/9/merge_requests/7/approval_state": status(404, `{"message":"404 Not found"}`),
	}))
	f, e := NewClient(s.URL, "", s.Client(), 1<<20).Collect(context.Background(), goodExpect())
	if e != nil || f.ApprovalSettingsKnown || f.RulesKnown {
		t.Fatalf("%+v %v", f, e)
	}
	if _, reason := Evaluate(f, goodExpect()); !strings.Contains(reason, "Premium") {
		t.Fatalf("reason %q", reason)
	}
}

func TestCollectPropagatesOperationalErrors(t *testing.T) {
	s := serve(t, collectRoutes(map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9": status(502, `{}`)}))
	if _, e := NewClient(s.URL, "", s.Client(), 1<<20).Collect(context.Background(), goodExpect()); e == nil {
		t.Fatal("server error hidden")
	}
}

func TestCollectSkipsPipelineCallsWithoutHeadPipeline(t *testing.T) {
	s := serve(t, collectRoutes(map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/merge_requests/7":   body(strings.Replace(mrJSON, `"head_pipeline":{"id":500}`, `"head_pipeline":null`, 1)),
		"GET /projects/9/pipelines/500":      nil,
		"GET /projects/9/pipelines/500/jobs": nil,
	}))
	f, e := NewClient(s.URL, "", s.Client(), 1<<20).Collect(context.Background(), goodExpect())
	if e != nil || f.Pipeline != nil {
		t.Fatalf("%+v %v", f, e)
	}
}

func TestCollectFetchesMergedResultParents(t *testing.T) {
	s := serve(t, collectRoutes(map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/pipelines/500":          body(`{"id":500,"project_id":9,"sha":"aaa","ref":"refs/merge-requests/7/merge","status":"success","source":"merge_request_event"}`),
		"GET /projects/9/repository/commits/aaa": body(`{"id":"aaa","parent_ids":["456","abc"]}`),
	}))
	f, e := NewClient(s.URL, "", s.Client(), 1<<20).Collect(context.Background(), goodExpect())
	if e != nil || len(f.PipelineParents) != 2 {
		t.Fatalf("%+v %v", f, e)
	}
}

// With advisory protections Collect never reads protection settings, so
// approvals also work on GitLab Free (no Premium-only call is made).
func TestCollectAdvisoryReadsNoProtectionSettings(t *testing.T) {
	forbid := func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("protection endpoint read: %s", r.URL.Path)
		w.WriteHeader(500)
	}
	s := serve(t, collectRoutes(map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9":                                 body(`{"id":9,"only_allow_merge_if_pipeline_succeeds":false,"allow_merge_on_skipped_pipeline":true,"merge_method":"merge","ci_config_path":".gitlab-ci.yml@group/ci-config"}`),
		"GET /projects/9/approvals":                       forbid,
		"GET /projects/9/protected_branches/main":         forbid,
		"GET /projects/9/merge_requests/7/approval_state": forbid,
	}))
	x := goodExpect()
	x.Protections = "advisory"
	f, e := NewClient(s.URL, "", s.Client(), 1<<20).Collect(context.Background(), x)
	if e != nil {
		t.Fatal(e)
	}
	if _, reason := Evaluate(f, x); reason != "" {
		t.Fatalf("withheld on native protection settings: %s", reason)
	}
}

// Protections are required unless explicitly configured as advisory. With
// advisory protections, every protection gap that withholds by default is
// approved instead.
func TestEvaluateAdvisoryProtections(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Facts)
	}{
		{"pipelines optional", func(f *Facts) { f.Project.PipelineMustSucceed = false }},
		{"skipped pipelines mergeable", func(f *Facts) { f.Project.AllowSkippedPipeline = true }},
		{"merge commit without merged results", func(f *Facts) { f.Project.MergeMethod = "merge" }},
		{"approval settings unavailable", func(f *Facts) { f.ApprovalSettingsKnown = false }},
		{"approvals survive push", func(f *Facts) { f.ApprovalSettings.ResetOnPush = false }},
		{"unprotected target", func(f *Facts) { f.Branch.Protected = false }},
		{"force push allowed", func(f *Facts) { f.Branch.AllowForcePush = true }},
		{"approval rules unavailable", func(f *Facts) { f.RulesKnown = false }},
		{"two humans still required", func(f *Facts) { f.Rules[0].Required = 2 }},
		{"bot not eligible", func(f *Facts) { f.Rules[0].EligibleIDs = []string{"6"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, x := goodFacts(), goodExpect()
			tc.mut(&f)
			if _, reason := Evaluate(f, x); reason == "" {
				t.Fatal("protection gap approved without opting in")
			}
			x.Protections = "advisory"
			if _, reason := Evaluate(f, x); reason != "" {
				t.Fatalf("advisory protections still withheld: %s", reason)
			}
		})
	}
}

func TestEvaluateRejectsUnknownProtectionMode(t *testing.T) {
	x := goodExpect()
	x.Protections = "off"
	if _, reason := Evaluate(goodFacts(), x); !strings.Contains(reason, "protection") {
		t.Fatalf("reason %q", reason)
	}
}
