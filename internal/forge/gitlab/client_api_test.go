package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const mrJSON = `{"id":88,"iid":7,"project_id":9,"source_project_id":9,"target_project_id":9,"state":"opened","draft":false,"sha":"abc","target_branch":"main","head_pipeline":{"id":500},"diff_refs":{"head_sha":"abc","base_sha":"def","start_sha":"123"}}`

// serve routes by escaped path so encoded branch names are matched exactly.
func serve(t *testing.T, routes map[string]func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.EscapedPath()
		if f, ok := routes[key]; ok {
			f(w, r)
			return
		}
		t.Errorf("unexpected %s", key)
		w.WriteHeader(404)
	}))
	t.Cleanup(s.Close)
	return s
}
func body(s string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(s)) }
}
func status(code int, s string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code); w.Write([]byte(s)) }
}

func TestChangeExposesStateDraftBranchAndPipeline(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/merge_requests/7":         body(mrJSON),
		"GET /projects/9/repository/branches/main": body(`{"name":"main","commit":{"id":"456"}}`),
	})
	got, e := NewClient(s.URL, "", s.Client(), 4096).Change(context.Background(), "9", 7)
	if e != nil || got.State != "opened" || got.Draft || got.TargetBranch != "main" || got.HeadPipelineID != 500 || got.TargetOID != "456" || got.MergeBaseOID != "def" {
		t.Fatalf("%+v %v", got, e)
	}
}

func TestChangeTreatsLegacyWorkInProgressAsDraft(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/merge_requests/7":         body(strings.Replace(mrJSON, `"draft":false`, `"work_in_progress":true`, 1)),
		"GET /projects/9/repository/branches/main": body(`{"name":"main","commit":{"id":"456"}}`),
	})
	got, e := NewClient(s.URL, "", s.Client(), 4096).Change(context.Background(), "9", 7)
	if e != nil || !got.Draft {
		t.Fatalf("legacy WIP not draft %+v %v", got, e)
	}
}

func TestChangeWithoutPipelineIsZero(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/merge_requests/7":         body(strings.Replace(mrJSON, `"head_pipeline":{"id":500}`, `"head_pipeline":null`, 1)),
		"GET /projects/9/repository/branches/main": body(`{"name":"main","commit":{"id":"456"}}`),
	})
	got, e := NewClient(s.URL, "", s.Client(), 4096).Change(context.Background(), "9", 7)
	if e != nil || got.HeadPipelineID != 0 {
		t.Fatalf("%+v %v", got, e)
	}
}

func TestEncodedBranchNamesAreSingleSegments(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/merge_requests/7":                  body(strings.Replace(mrJSON, `"target_branch":"main"`, `"target_branch":"release/1.0"`, 1)),
		"GET /projects/9/repository/branches/release%2F1.0": body(`{"name":"release/1.0","commit":{"id":"456"}}`),
		"GET /projects/9/protected_branches/release%2F1.0":  body(`{"name":"release/1.0","allow_force_push":false,"code_owner_approval_required":true}`),
	})
	c := NewClient(s.URL, "", s.Client(), 4096)
	if got, e := c.Change(context.Background(), "9", 7); e != nil || got.TargetBranch != "release/1.0" {
		t.Fatalf("%+v %v", got, e)
	}
	p, e := c.ProtectedBranch(context.Background(), "9", "release/1.0")
	if e != nil || !p.Protected || p.AllowForcePush || !p.CodeOwnerApprovalRequired {
		t.Fatalf("%+v %v", p, e)
	}
}

func TestAuthenticationHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, token            string
		bearer                 bool
		private, authorization string
	}{{"private token", "tok", false, "tok", ""}, {"bearer token", "tok", true, "", "Bearer tok"}, {"no token sends no credential", "", false, "", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, ok := r.Header["Private-Token"]; ok != (tc.private != "") || r.Header.Get("Private-Token") != tc.private {
					t.Errorf("PRIVATE-TOKEN %q present=%v", r.Header.Get("Private-Token"), ok)
				}
				if r.Header.Get("Authorization") != tc.authorization {
					t.Errorf("Authorization %q", r.Header.Get("Authorization"))
				}
				w.Write([]byte(`{"id":5}`))
			}))
			defer s.Close()
			c := NewClient(s.URL, tc.token, s.Client(), 4096)
			if tc.bearer {
				c = NewBearerClient(s.URL, tc.token, s.Client(), 4096)
			}
			if id, e := c.CurrentUser(context.Background()); e != nil || id != "5" {
				t.Fatalf("%s %v", id, e)
			}
		})
	}
}

func TestCurrentUserRequiresIdentity(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /user": body(`{}`)})
	if _, e := NewClient(s.URL, "", s.Client(), 4096).CurrentUser(context.Background()); e == nil {
		t.Fatal("missing user identity accepted")
	}
}

func TestStatusErrorsAreTyped(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /user": status(403, `{}`)})
	_, e := NewClient(s.URL, "", s.Client(), 4096).CurrentUser(context.Background())
	var se *StatusError
	if !errors.As(e, &se) || se.Code != 403 {
		t.Fatalf("untyped status error %v", e)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /user": func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.invalid/user", http.StatusFound)
	}})
	if _, e := NewClient(s.URL, "secret", s.Client(), 4096).CurrentUser(context.Background()); e == nil {
		t.Fatal("redirect followed or accepted")
	}
}

func TestPipelineParsesAndValidates(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/pipelines/500": body(`{"id":500,"project_id":9,"sha":"abc","ref":"refs/merge-requests/7/head","status":"success","source":"merge_request_event"}`),
		"GET /projects/9/pipelines/501": body(`{"id":999,"project_id":9,"sha":"abc","ref":"main","status":"success","source":"push"}`),
	})
	c := NewClient(s.URL, "", s.Client(), 4096)
	p, e := c.Pipeline(context.Background(), "9", 500)
	if e != nil || p.ID != 500 || p.ProjectID != "9" || p.SHA != "abc" || p.Ref != "refs/merge-requests/7/head" || p.Status != "success" || p.Source != "merge_request_event" {
		t.Fatalf("%+v %v", p, e)
	}
	if _, e := c.Pipeline(context.Background(), "9", 501); e == nil {
		t.Fatal("pipeline identity mismatch accepted")
	}
	if _, e := c.Pipeline(context.Background(), "9", 0); e == nil {
		t.Fatal("zero pipeline ID accepted")
	}
}

func TestPipelineJobsPaginateLatestAttemptsAndBindPipeline(t *testing.T) {
	page := func(n, start int) string {
		var xs []string
		for i := 0; i < n; i++ {
			xs = append(xs, fmt.Sprintf(`{"id":%d,"name":"job-%d","status":"success","allow_failure":false,"pipeline":{"id":500,"project_id":9}}`, start+i, start+i))
		}
		return "[" + strings.Join(xs, ",") + "]"
	}
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/pipelines/500/jobs": func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("include_retried") == "true" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("page") == "1" {
			w.Write([]byte(page(100, 0)))
		} else {
			w.Write([]byte(page(3, 100)))
		}
	}})
	jobs, e := NewClient(s.URL, "", s.Client(), 1<<20).PipelineJobs(context.Background(), "9", 500, 5)
	if e != nil || len(jobs) != 103 || jobs[102].Name != "job-102" || jobs[0].PipelineID != 500 {
		t.Fatalf("%d %v", len(jobs), e)
	}
}

func TestPipelineJobsRejectForeignPipelineAndCap(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/pipelines/500/jobs": body(`[{"id":1,"name":"ci","status":"success","pipeline":{"id":501,"project_id":9}}]`)})
	if _, e := NewClient(s.URL, "", s.Client(), 4096).PipelineJobs(context.Background(), "9", 500, 5); e == nil {
		t.Fatal("job from another pipeline accepted")
	}
	full := "[" + strings.Repeat(`{"id":1,"name":"ci","status":"success","pipeline":{"id":500,"project_id":9}},`, 99) + `{"id":1,"name":"ci","status":"success","pipeline":{"id":500,"project_id":9}}]`
	s2 := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/pipelines/500/jobs": body(full)})
	if _, e := NewClient(s2.URL, "", s2.Client(), 1<<20).PipelineJobs(context.Background(), "9", 500, 2); e == nil {
		t.Fatal("pagination cap ignored")
	}
}

func TestCommitParents(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9/repository/commits/aaa": body(`{"id":"aaa","parent_ids":["456","abc"]}`),
		"GET /projects/9/repository/commits/bbb": body(`{"id":"ccc","parent_ids":["456"]}`),
	})
	c := NewClient(s.URL, "", s.Client(), 4096)
	ps, e := c.CommitParents(context.Background(), "9", "aaa")
	if e != nil || len(ps) != 2 || ps[0] != "456" || ps[1] != "abc" {
		t.Fatalf("%v %v", ps, e)
	}
	if _, e := c.CommitParents(context.Background(), "9", "bbb"); e == nil {
		t.Fatal("commit identity mismatch accepted")
	}
	if _, e := c.CommitParents(context.Background(), "9", "../x"); e == nil {
		t.Fatal("non-OID revision accepted")
	}
}

func TestProjectAndApprovalSettings(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/9":           body(`{"id":9,"only_allow_merge_if_pipeline_succeeds":true,"allow_merge_on_skipped_pipeline":false,"merge_method":"ff","merge_pipelines_enabled":true,"merge_trains_enabled":false}`),
		"GET /projects/9/approvals": body(`{"reset_approvals_on_push":true,"selective_code_owner_removals":false}`),
	})
	c := NewClient(s.URL, "", s.Client(), 4096)
	p, e := c.ProjectSettings(context.Background(), "9")
	if e != nil || !p.PipelineMustSucceed || p.AllowSkippedPipeline || p.MergeMethod != "ff" || !p.MergedResultsPipelines || p.MergeTrains {
		t.Fatalf("%+v %v", p, e)
	}
	a, e := c.ApprovalSettings(context.Background(), "9")
	if e != nil || !a.ResetOnPush || a.SelectiveCodeOwnerRemovals {
		t.Fatalf("%+v %v", a, e)
	}
}

func TestProjectSettingsRequireIdentity(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9": body(`{"id":10,"merge_method":"ff"}`)})
	if _, e := NewClient(s.URL, "", s.Client(), 4096).ProjectSettings(context.Background(), "9"); e == nil {
		t.Fatal("other project's settings accepted")
	}
}

func TestUnprotectedBranchIsKnownFalse(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/protected_branches/main": status(404, `{"message":"404 Not found"}`)})
	p, e := NewClient(s.URL, "", s.Client(), 4096).ProtectedBranch(context.Background(), "9", "main")
	if e != nil || p.Protected {
		t.Fatalf("%+v %v", p, e)
	}
	s2 := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/protected_branches/main": status(500, `{}`)})
	if _, e := NewClient(s2.URL, "", s2.Client(), 4096).ProtectedBranch(context.Background(), "9", "main"); e == nil {
		t.Fatal("server error treated as unprotected")
	}
}

func TestApprovalStateRules(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /projects/9/merge_requests/7/approval_state": body(`{"approval_rules_overwritten":false,"rules":[
		{"id":1,"name":"Backend","rule_type":"regular","approvals_required":2,"approved":false,"eligible_approvers":[{"id":5},{"id":6}],"approved_by":[{"id":6}]},
		{"id":2,"name":"All Members","rule_type":"any_approver","approvals_required":1,"approved":true,"eligible_approvers":[],"approved_by":[{"id":6}]}]}`)})
	rules, e := NewClient(s.URL, "", s.Client(), 4096).ApprovalState(context.Background(), "9", 7)
	if e != nil || len(rules) != 2 {
		t.Fatalf("%+v %v", rules, e)
	}
	if r := rules[0]; r.Name != "Backend" || r.Type != "regular" || r.Required != 2 || r.Approved || len(r.EligibleIDs) != 2 || r.EligibleIDs[0] != "5" || len(r.ApprovedByIDs) != 1 || r.ApprovedByIDs[0] != "6" {
		t.Fatalf("%+v", r)
	}
	if r := rules[1]; r.Type != "any_approver" || !r.Approved {
		t.Fatalf("%+v", r)
	}
}

func approveServer(t *testing.T, code int, approveBody string, approvalsBody string, gotSHA *string) *httptest.Server {
	return serve(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /projects/9/merge_requests/7/approve": func(w http.ResponseWriter, r *http.Request) {
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			*gotSHA = b["sha"]
			w.WriteHeader(code)
			w.Write([]byte(approveBody))
		},
		"GET /projects/9/merge_requests/7/approvals": body(approvalsBody),
	})
}

func TestApproveSucceedsWhenBotRecordedEvenIfMoreApprovalsRequired(t *testing.T) {
	var sha string
	// GitLab's approved flag means the MR is fully approved, not that our write applied.
	s := approveServer(t, 201, `{"id":88,"iid":7,"approved":false,"approvals_left":1,"approved_by":[{"user":{"id":5}}]}`, `{}`, &sha)
	id, e := NewClient(s.URL, "", s.Client(), 4096).Approve(context.Background(), "9", 7, "abc", "5")
	if e != nil || sha != "abc" || id != "gitlab:9:7:abc:5" {
		t.Fatalf("%q %v sha=%q", id, e, sha)
	}
}

func TestApproveWithoutBotInResponseIsAmbiguous(t *testing.T) {
	var sha string
	s := approveServer(t, 201, `{"id":88,"approved":true,"approved_by":[{"user":{"id":6}}]}`, `{}`, &sha)
	_, e := NewClient(s.URL, "", s.Client(), 4096).Approve(context.Background(), "9", 7, "abc", "5")
	if e == nil || errors.Is(e, ErrRejected) {
		t.Fatalf("missing bot approval must be ambiguous, got %v", e)
	}
}

func TestApproveAlreadyApprovedIsIdempotent(t *testing.T) {
	var sha string
	s := approveServer(t, 401, `{"message":"401 Unauthorized"}`, `{"user_has_approved":true,"approved_by":[{"user":{"id":5}}]}`, &sha)
	id, e := NewClient(s.URL, "", s.Client(), 4096).Approve(context.Background(), "9", 7, "abc", "5")
	if e != nil || id != "gitlab:9:7:abc:5" {
		t.Fatalf("%q %v", id, e)
	}
}

func TestApproveUnauthorizedWithoutApprovalIsRejected(t *testing.T) {
	var sha string
	s := approveServer(t, 401, `{}`, `{"user_has_approved":false,"approved_by":[]}`, &sha)
	if _, e := NewClient(s.URL, "", s.Client(), 4096).Approve(context.Background(), "9", 7, "abc", "5"); !errors.Is(e, ErrRejected) {
		t.Fatalf("expected definite rejection, got %v", e)
	}
}

func TestApproveDefiniteRejections(t *testing.T) {
	for _, code := range []int{403, 404, 409, 422} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var sha string
			s := approveServer(t, code, `{}`, `{}`, &sha)
			if _, e := NewClient(s.URL, "", s.Client(), 4096).Approve(context.Background(), "9", 7, "abc", "5"); !errors.Is(e, ErrRejected) {
				t.Fatalf("expected definite rejection, got %v", e)
			}
		})
	}
}

func TestApproveServerErrorIsAmbiguous(t *testing.T) {
	var sha string
	s := approveServer(t, 502, `{}`, `{}`, &sha)
	_, e := NewClient(s.URL, "", s.Client(), 4096).Approve(context.Background(), "9", 7, "abc", "5")
	if e == nil || errors.Is(e, ErrRejected) {
		t.Fatalf("5xx must be ambiguous, got %v", e)
	}
}

func TestApproveValidatesIdentity(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", nil, 4096)
	for _, args := range [][]string{{"x", "abc", "5"}, {"9", "", "5"}, {"9", "abc", ""}, {"9", "abc", "bot"}} {
		if _, e := c.Approve(context.Background(), args[0], 7, args[1], args[2]); e == nil || errors.Is(e, ErrRejected) == false && !strings.Contains(e.Error(), "identity") {
			t.Fatalf("%v accepted: %v", args, e)
		}
	}
}

func TestResponseCapEnforced(t *testing.T) {
	s := serve(t, map[string]func(http.ResponseWriter, *http.Request){"GET /user": body(`{"id":5,"pad":"` + strings.Repeat("x", 200) + `"}`)})
	if _, e := NewClient(s.URL, "", s.Client(), 64).CurrentUser(context.Background()); e == nil {
		t.Fatal("oversized response accepted")
	}
}
