package main

import (
	"strings"
)

// changeFacts are the API-captured facts automatic review triggers decide
// on. They are untrusted text: compared, never rendered.
type changeFacts struct {
	Draft         bool
	Author, Title string
	Labels        []string
}

// autoSkipReason returns why the service should not review a change
// automatically (draft, ignored_author, skip_label, skip_title_keyword), or
// "" to review it.
func autoSkipReason(c forgeConfig, f changeFacts) string {
	if f.Draft && c.AutoDrafts != "review" {
		return "draft"
	}
	for _, p := range c.AutoIgnoreAuthors {
		if authorMatches(p, f.Author) {
			return "ignored_author"
		}
	}
	for _, want := range c.AutoSkipLabels {
		for _, l := range f.Labels {
			if strings.EqualFold(want, l) {
				return "skip_label"
			}
		}
	}
	title := strings.ToLower(f.Title)
	for _, k := range c.AutoSkipTitleKeywords {
		if strings.Contains(title, strings.ToLower(k)) {
			return "skip_title_keyword"
		}
	}
	return ""
}

// authorMatches matches a login against a pattern in which * matches any run
// of characters and nothing else is special. Case is ignored.
func authorMatches(pattern, login string) bool {
	if login == "" {
		return false
	}
	parts := strings.Split(strings.ToLower(pattern), "*")
	s := strings.ToLower(login)
	if len(parts) == 1 {
		return s == parts[0]
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return len(s) >= len(last) && strings.HasSuffix(s, last)
}

// parseReviewCommand reports whether a comment body's first non-blank line
// is exactly a review command: "@<bot> review", or on GitHub also
// "/changeharbor review". Fields are compared case-insensitively, and one
// trailing "[bot]" is ignored on both usernames.
func parseReviewCommand(provider, body, botUsername string) bool {
	for _, line := range strings.Split(body, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 2 || !strings.EqualFold(f[1], "review") {
			return false
		}
		if provider == "github" && strings.EqualFold(f[0], "/changeharbor") {
			return true
		}
		name, ok := strings.CutPrefix(f[0], "@")
		if !ok || botUsername == "" {
			return false
		}
		trim := func(u string) string {
			u = strings.ToLower(u)
			return strings.TrimSuffix(u, "[bot]")
		}
		return name != "" && trim(name) == trim(botUsername)
	}
	return false
}

// githubPermissionAuthorized reports whether a GitHub user with this
// repository permission may request a review: the base permission must be
// exactly admin or write, and the role name (which can only veto) must not
// be read, triage or none. A custom role reports its base permission; an
// absent role name ("") does not veto.
func githubPermissionAuthorized(permission, roleName string) bool {
	if permission != "admin" && permission != "write" {
		return false
	}
	switch roleName {
	case "read", "triage", "none":
		return false
	}
	return true
}

// commandAuthorized reports whether a comment author may request a review:
// on GitHub an OWNER, MEMBER or COLLABORATOR association; on GitLab at least
// Developer access (30).
func commandAuthorized(provider, association string, accessLevel int) bool {
	switch provider {
	case "github":
		return association == "OWNER" || association == "MEMBER" || association == "COLLABORATOR"
	case "gitlab":
		return accessLevel >= 30
	}
	return false
}

type healthRepository struct {
	Approvals      string `json:"approvals"`
	Comments       string `json:"comments"`
	InlineComments string `json:"inline_comments"`
}

type healthReport struct {
	Status       string                      `json:"status"`
	Processing   string                      `json:"processing"`
	Repositories map[string]healthRepository `json:"repositories"`
}

// healthStatus reports each mapping's effective write modes, in the
// vocabulary of config check. It names no paths, URLs or variables.
func healthStatus(c serverConfig) healthReport {
	out := healthReport{Status: "ok", Processing: "durable_worker", Repositories: map[string]healthRepository{}}
	for key, o := range c.Repositories {
		forge := o.forge
		if forge == nil && o.ForgeConfig != "" {
			fc, e := loadForgeConfig(o.ForgeConfig)
			if e != nil {
				out.Repositories[key] = healthRepository{"unknown", "unknown", "unknown"}
				continue
			}
			forge = &fc
		}
		if forge == nil {
			out.Repositories[key] = healthRepository{"off", "off", "off"}
			continue
		}
		eff := forge.effective()
		out.Repositories[key] = healthRepository{eff["approvals"].(string), eff["comments"].(string), eff["inline_comments"].(string)}
	}
	return out
}
