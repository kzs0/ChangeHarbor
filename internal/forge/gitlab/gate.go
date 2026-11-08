package gitlab

import (
	"context"
	"fmt"
	"slices"
)

// Expect is what the host already established: the reviewed revisions, the
// locally computed merge base, the trusted mapping and the bot identity.
// RequiredJobs maps a job name to its trusted producer, "project:<id>".
type Expect struct {
	ProjectID                                        string
	IID                                              int
	Head, Target, MergeBase, TargetBranch, BotUserID string
	RequiredJobs                                     map[string]string
}

// Facts are live GitLab observations. Known flags separate "unavailable"
// (for example Premium-only endpoints on Free) from a definite false.
type Facts struct {
	Change                Change
	UserID                string
	Project               ProjectSettings
	ApprovalSettings      ApprovalSettings
	ApprovalSettingsKnown bool
	Branch                ProtectedBranch
	Rules                 []ApprovalRule
	RulesKnown            bool
	Pipeline              *Pipeline
	PipelineParents       []string
	Jobs                  []Job
}

func unavailable(e error) bool { c := statusCode(e); return c == 403 || c == 404 }

// Collect reads every fact Evaluate needs. Premium-only endpoints that answer
// 403/404 are recorded as unknown; any other failure is returned.
func (c *Client) Collect(ctx context.Context, x Expect) (Facts, error) {
	var f Facts
	var e error
	if f.Change, e = c.Change(ctx, x.ProjectID, x.IID); e != nil {
		return f, e
	}
	if f.UserID, e = c.CurrentUser(ctx); e != nil {
		return f, e
	}
	if f.Project, e = c.ProjectSettings(ctx, x.ProjectID); e != nil {
		return f, e
	}
	if f.ApprovalSettings, e = c.ApprovalSettings(ctx, x.ProjectID); e == nil {
		f.ApprovalSettingsKnown = true
	} else if !unavailable(e) {
		return f, e
	}
	if f.Branch, e = c.ProtectedBranch(ctx, x.ProjectID, x.TargetBranch); e != nil {
		return f, e
	}
	if f.Rules, e = c.ApprovalState(ctx, x.ProjectID, x.IID); e == nil {
		f.RulesKnown = true
	} else if !unavailable(e) {
		return f, e
	}
	if f.Change.HeadPipelineID == 0 {
		return f, nil
	}
	p, e := c.Pipeline(ctx, x.ProjectID, f.Change.HeadPipelineID)
	if e != nil {
		return f, e
	}
	f.Pipeline = &p
	if p.SHA != f.Change.HeadOID {
		if f.PipelineParents, e = c.CommitParents(ctx, x.ProjectID, p.SHA); e != nil {
			return f, e
		}
	}
	if f.Jobs, e = c.PipelineJobs(ctx, x.ProjectID, p.ID, 20); e != nil {
		return f, e
	}
	return f, nil
}

var trustedSources = []string{"merge_request_event", "push"}

// Evaluate returns the required jobs that passed, or a withholding reason.
// It never returns partial evidence alongside a reason.
func Evaluate(f Facts, x Expect) (map[string]bool, string) {
	ch := f.Change
	switch {
	case ch.HeadOID != x.Head || ch.TargetOID != x.Target:
		return nil, "reviewed HEAD or target changed"
	case ch.State != "opened":
		return nil, "merge request is not open"
	case ch.Draft:
		return nil, "merge request is a draft"
	case ch.TargetBranch != x.TargetBranch:
		return nil, "merge request targets a different branch than the trusted mapping"
	case x.MergeBase == "" || ch.MergeBaseOID != x.MergeBase:
		return nil, "GitLab merge request diff is not computed against the live target"
	case x.BotUserID == "" || f.UserID != x.BotUserID:
		return nil, "credential does not act as the configured bot user"
	case !f.Project.PipelineMustSucceed:
		return nil, "pipelines are not required to succeed before merge"
	case f.Project.AllowSkippedPipeline:
		return nil, "skipped pipelines may be merged"
	case !(f.Project.MergeMethod == "ff" || f.Project.MergeMethod == "rebase_merge" || f.Project.MergedResultsPipelines):
		return nil, "target freshness is not enforced natively (fast-forward, semi-linear or merged results pipelines required)"
	case !f.ApprovalSettingsKnown || !f.RulesKnown:
		return nil, "approval settings or rules unavailable; GitLab Premium approval controls are required"
	case !f.ApprovalSettings.ResetOnPush || f.ApprovalSettings.SelectiveCodeOwnerRemovals:
		return nil, "approvals are not reset on push"
	case !f.Branch.Protected:
		return nil, "target branch is not protected"
	case f.Branch.AllowForcePush:
		return nil, "target branch allows force push"
	}
	for _, r := range f.Rules {
		if r.Approved || r.Required <= 0 {
			continue
		}
		eligible := r.Type == "any_approver" || slices.Contains(r.EligibleIDs, x.BotUserID)
		left := r.Required - len(r.ApprovedByIDs)
		if !eligible || left > 1 || slices.Contains(r.ApprovedByIDs, x.BotUserID) {
			return nil, fmt.Sprintf("native approval rule %q requires additional human approval", r.Name)
		}
	}
	p := f.Pipeline
	if ch.HeadPipelineID == 0 || p == nil {
		return nil, "merge request has no head pipeline"
	}
	if p.ProjectID != x.ProjectID || !slices.Contains(trustedSources, p.Source) {
		return nil, "head pipeline source or project is not trusted"
	}
	mergedRef := fmt.Sprintf("refs/merge-requests/%d/merge", x.IID)
	bound := p.ID == ch.HeadPipelineID && (p.SHA == x.Head ||
		(p.Ref == mergedRef && slices.Equal(f.PipelineParents, []string{x.Target, x.Head})))
	if !bound {
		return nil, "head pipeline is not bound to the reviewed HEAD and live target"
	}
	if p.Status != "success" {
		return nil, "head pipeline has not succeeded"
	}
	passed := map[string]bool{}
	for name, producer := range x.RequiredJobs {
		if producer == "" || producer != "project:"+x.ProjectID {
			return nil, fmt.Sprintf("required job %q has an untrusted producer mapping", name)
		}
		var found []Job
		for _, j := range f.Jobs {
			if j.Name == name && j.PipelineID == p.ID {
				found = append(found, j)
			}
		}
		switch {
		case len(found) == 0:
			return nil, fmt.Sprintf("required trusted job %q is missing", name)
		case len(found) > 1:
			return nil, fmt.Sprintf("required job %q is ambiguous", name)
		case found[0].Status != "success":
			return nil, fmt.Sprintf("required job %q failed or did not run", name)
		}
		passed[name] = true
	}
	return passed, ""
}
