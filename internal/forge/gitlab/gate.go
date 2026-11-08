package gitlab

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/kzs0/reviewd/internal/policy"
)

// Expect is what the host already established: the reviewed revisions, the
// locally computed merge base, the trusted mapping and the bot identity.
// RequiredJobs maps a job name to its trusted producer, "project:<id>".
type Expect struct {
	ProjectID                                        string
	IID                                              int
	Head, Target, MergeBase, TargetBranch, BotUserID string
	RequiredJobs                                     map[string]string
	// CI configuration trust: CITrust is "external_config" or
	// "unchanged_config"; ProjectPath is the project's web path; ChangedPaths
	// are the reviewed diff's old and new paths; CIPaths are extra globs that
	// define job behaviour.
	CITrust, ProjectPath  string
	ChangedPaths, CIPaths []string
}

// Facts are the live GitLab observations approval depends on. Native
// protection settings are deliberately absent: approvals are submitted
// whenever configured, and ChangeHarbor withdraws its own approval when a
// merge request moves (see revalidation) instead of relying on the forge.
type Facts struct {
	Change          Change
	UserID          string
	Project         ProjectSettings
	Pipeline        *Pipeline
	PipelineParents []string
	Jobs            []Job
}

func unavailable(e error) bool { c := statusCode(e); return c == 403 || c == 404 }

// Collect reads every fact Evaluate needs. None of its endpoints are
// Premium-only, so it works on every GitLab tier.
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
	}
	if reason := ciTrust(f.Project.CIConfigPath, x); reason != "" {
		return nil, reason
	}
	p := f.Pipeline
	if ch.HeadPipelineID == 0 || p == nil {
		return nil, "merge request has no head pipeline"
	}
	if p.ProjectID != x.ProjectID && p.ProjectID == ch.SourceRepositoryID {
		return nil, "head pipeline ran in the fork project and is not trusted; a maintainer must run the merge request pipeline in the parent project"
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

// ExternalCIConfig reports whether ci_config_path loads the pipeline
// definition from a different project ("file@group/project[:ref]").
func ExternalCIConfig(ciConfigPath, projectPath string) bool {
	file, ref, ok := strings.Cut(ciConfigPath, "@")
	if !ok || file == "" {
		return false
	}
	project, _, _ := strings.Cut(ref, ":")
	return project != "" && !strings.EqualFold(strings.Trim(project, "/"), strings.Trim(projectPath, "/"))
}

func ciTrust(ciConfigPath string, x Expect) string {
	switch x.CITrust {
	case "external_config":
		if !ExternalCIConfig(ciConfigPath, x.ProjectPath) {
			return "CI configuration is not loaded from a separate project (ci_config_path must be file@group/other-project)"
		}
		return ""
	case "unchanged_config":
		patterns := append([]string{".gitlab-ci.yml"}, x.CIPaths...)
		if ciConfigPath != "" && !strings.Contains(ciConfigPath, "@") {
			patterns = append(patterns, ciConfigPath)
		}
		for _, changed := range x.ChangedPaths {
			for _, pattern := range patterns {
				if ok, e := policy.Match(pattern, changed); e != nil || ok {
					return fmt.Sprintf("merge request changes CI configuration (%s); human review required", changed)
				}
			}
		}
		return ""
	case "":
		return "CI configuration trust is not configured"
	default:
		return "unknown CI configuration trust mode"
	}
}
