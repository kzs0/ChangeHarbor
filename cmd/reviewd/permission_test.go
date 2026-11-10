package main

// Unit tests for GitHub review command authorisation by repository
// permission (docs/design/review-feedback-and-triggers.md, addendum A3).

import "testing"

func TestGitHubPermissionAuthorized(t *testing.T) {
	for _, tc := range []struct {
		permission, role string
		want             bool
	}{
		{"admin", "admin", true},
		{"write", "maintain", true},
		{"write", "write", true},
		{"write", "security-engineer", true}, // custom role based on write
		{"admin", "org-admin-custom", true},
		{"write", "", true}, // role_name absent
		{"admin", "", true},
		{"read", "read", false},
		{"read", "triage", false},
		{"read", "", false},
		{"none", "none", false},
		{"", "", false},
		{"", "admin", false}, // role_name alone never authorises
		{"", "write", false},
		{"write", "triage", false}, // role_name vetoes an inconsistent base
		{"write", "read", false},
		{"admin", "none", false},
		{"Write", "write", false}, // exact values only
		{"ADMIN", "admin", false},
		{"maintain", "maintain", false}, // permission is the base level; maintain is reported as write
		{"triage", "triage", false},
	} {
		if got := githubPermissionAuthorized(tc.permission, tc.role); got != tc.want {
			t.Errorf("githubPermissionAuthorized(%q, %q) = %v, want %v", tc.permission, tc.role, got, tc.want)
		}
	}
}
