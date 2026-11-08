package gitlab

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Check is one deployment preflight result. Errors block approval
// publication; warnings explain predictable withholding or excess privilege.
type Check struct {
	Name     string
	OK       bool
	Severity string `json:",omitempty"`
	Detail   string
}

type DiagnoseInput struct {
	ProjectID, ProjectPath, Branch, BotUserID, CITrust string
	Now                                                time.Time
}

// Failed reports whether any check failed with error severity.
func Failed(cs []Check) bool {
	for _, c := range cs {
		if !c.OK && c.Severity == "error" {
			return true
		}
	}
	return false
}

func pass(name, detail string) Check { return Check{Name: name, OK: true, Detail: detail} }
func fail(name, severity, detail string) Check {
	return Check{Name: name, Severity: severity, Detail: detail}
}

type projectRule struct {
	Name     string    `json:"name"`
	Type     string    `json:"rule_type"`
	Required int       `json:"approvals_required"`
	Eligible []userRef `json:"eligible_approvers"`
}

// Diagnose reads token, membership and project settings without writing.
// Premium endpoints that answer 403/404 become failed checks; other API
// failures are returned as errors.
func (c *Client) Diagnose(ctx context.Context, in DiagnoseInput) ([]Check, error) {
	if !numeric.MatchString(in.ProjectID) || in.Branch == "" {
		return nil, errors.New("invalid diagnose request")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	var out []Check
	user, e := c.CurrentUser(ctx)
	if e != nil {
		return nil, e
	}
	if in.BotUserID != "" && user == in.BotUserID {
		out = append(out, pass("token_identity", "token acts as user "+user))
	} else {
		out = append(out, fail("token_identity", "error", fmt.Sprintf("token acts as user %s, bot_user_id is %q", user, in.BotUserID)))
	}

	var token struct {
		Active    bool     `json:"active"`
		Revoked   bool     `json:"revoked"`
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expires_at"`
	}
	switch e := c.request(ctx, "GET", "/personal_access_tokens/self", nil, &token); {
	case e != nil && !unavailable(e):
		return nil, e
	case e != nil:
		out = append(out, fail("token_scopes", "error", "token details unavailable"))
	case !token.Active || token.Revoked:
		out = append(out, fail("token_scopes", "error", "token is inactive or revoked"))
	case !slices.Contains(token.Scopes, "api"):
		out = append(out, fail("token_scopes", "error", fmt.Sprintf("token scopes %v lack api (required to approve and comment)", token.Scopes)))
	default:
		if exp, err := time.Parse("2006-01-02", token.ExpiresAt); err == nil && exp.Before(now.Add(14*24*time.Hour)) {
			out = append(out, fail("token_scopes", "warning", "token expires "+token.ExpiresAt))
		} else {
			out = append(out, pass("token_scopes", "api scope present"))
		}
	}

	subject := in.BotUserID
	if !numeric.MatchString(subject) {
		subject = user
	}
	var member struct {
		AccessLevel int `json:"access_level"`
	}
	switch e := c.request(ctx, "GET", fmt.Sprintf("/projects/%s/members/all/%s", in.ProjectID, subject), nil, &member); {
	case e != nil && statusCode(e) != 404:
		return nil, e
	case e != nil:
		out = append(out, fail("project_role", "error", "bot user is not a project member"))
	case member.AccessLevel < 30:
		out = append(out, fail("project_role", "error", fmt.Sprintf("access level %d; Developer (30) is required to approve", member.AccessLevel)))
	case member.AccessLevel >= 40:
		out = append(out, fail("project_role", "warning", fmt.Sprintf("access level %d can merge; Developer (30) is sufficient", member.AccessLevel)))
	default:
		out = append(out, pass("project_role", "Developer"))
	}

	p, e := c.ProjectSettings(ctx, in.ProjectID)
	if e != nil {
		return nil, e
	}
	out = append(out, cond(p.PipelineMustSucceed, "pipelines_required", "error", "pipelines must succeed before merge", "enable 'Pipelines must succeed'"))
	out = append(out, cond(!p.AllowSkippedPipeline, "skipped_pipelines", "error", "skipped pipelines cannot be merged", "disable 'Skipped pipelines are considered successful'"))
	out = append(out, cond(p.MergeMethod == "ff" || p.MergeMethod == "rebase_merge" || p.MergedResultsPipelines, "target_freshness", "error",
		"merge method "+p.MergeMethod, "use fast-forward or semi-linear merges, or enable merged results pipelines"))
	out = append(out, cond(!p.MergeTrains, "merge_trains", "warning", "merge trains disabled", "merge train pipelines cannot be bound to the reviewed commit; approvals will be withheld"))

	a, e := c.ApprovalSettings(ctx, in.ProjectID)
	switch {
	case e != nil && !unavailable(e):
		return nil, e
	case e != nil:
		out = append(out, fail("approval_reset", "error", "approval settings unavailable; GitLab Premium is required"))
	default:
		out = append(out, cond(a.ResetOnPush && !a.SelectiveCodeOwnerRemovals, "approval_reset", "error", "approvals reset on push", "enable 'Remove all approvals when commits are added'"))
	}

	b, e := c.ProtectedBranch(ctx, in.ProjectID, in.Branch)
	if e != nil {
		return nil, e
	}
	out = append(out, cond(b.Protected && !b.AllowForcePush, "branch_protection", "error", "protected without force push", "protect "+in.Branch+" and disallow force push"))

	var rules []projectRule
	switch e := c.request(ctx, "GET", "/projects/"+in.ProjectID+"/approval_rules?per_page=100", nil, &rules); {
	case e != nil && !unavailable(e):
		return nil, e
	case e != nil:
		out = append(out, fail("approval_rules", "error", "approval rules unavailable; GitLab Premium is required"))
	default:
		var blocked []string
		for _, r := range rules {
			if r.Required <= 0 || r.Type == "any_approver" {
				continue
			}
			if r.Required > 1 || !slices.Contains(ids(r.Eligible), in.BotUserID) {
				blocked = append(blocked, r.Name)
			}
		}
		if len(blocked) > 0 {
			out = append(out, fail("approval_rules", "warning", "bot cannot satisfy rules "+strings.Join(blocked, ", ")+"; approvals will be withheld"))
		} else {
			out = append(out, pass("approval_rules", "bot can satisfy every required rule"))
		}
	}

	switch in.CITrust {
	case "external_config":
		out = append(out, cond(ExternalCIConfig(p.CIConfigPath, in.ProjectPath), "ci_trust", "error",
			"CI configuration from "+p.CIConfigPath+"; protect that project's ref", "set ci_config_path to file@group/other-project"))
	case "unchanged_config":
		out = append(out, fail("ci_trust", "warning", "in-repo CI configuration; MRs touching ci_paths are withheld, so ci_paths must cover every file that defines job behaviour"))
	default:
		out = append(out, fail("ci_trust", "error", "ci_trust is not configured"))
	}
	return out, nil
}

func cond(ok bool, name, severity, good, bad string) Check {
	if ok {
		return pass(name, good)
	}
	return fail(name, severity, bad)
}
