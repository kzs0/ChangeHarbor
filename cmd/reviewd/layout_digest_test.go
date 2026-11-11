package main

// Policy digests across policy forms (docs/design/config-v1-layout.md,
// section 4): a JSON file keeps today's digest byte for byte, and a YAML
// file or inline mapping with the same content has the same digest.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kzs0/reviewd/internal/config"
	"github.com/kzs0/reviewd/internal/policy"
)

// richPolicyDigest is today's fileDigest of richPolicyJSON.
const richPolicyDigest = "9b35b3678c8b4c3419962fc5af1232b8de53d8dc5de9cb332b380ed9769bef67"

const richPolicyJSON = `{"base_minimum":1,"auto_approve_max":3,"required_checks":["test"],"path_rules":[{"id":"auth","paths":["auth/**"],"minimum":8,"explanation":"Auth code"}],"unknown_size":{"action":"minimum","minimum":7}}`

const richPolicyYAML = `# merge risk policy
base_minimum: 1
auto_approve_max: 3
required_checks: [test]
path_rules:
  - id: auth
    explanation: Auth code
    paths: ["auth/**"]
    minimum: 8
unknown_size:
  action: minimum
  minimum: 7
`

func TestPolicyDigestIsTheSameForEveryForm(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if e := os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	if d, e := fileDigest(write("fixture.json", `{"base_minimum":1,"auto_approve_max":3,"required_checks":["test"]}`)); e != nil || d != fixturePolicyDigest {
		t.Fatalf("fixture JSON digest changed: %s %v", d, e)
	}
	inline := func(body string) policy.Policy {
		t.Helper()
		doc := "version: 1\nrepositories:\n  app:\n    url: https://gitlab.com/group/app\n    auth: {token_env: T}\n    mirror: {path: /m}\n    policy:\n      " +
			strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n      ") + "\n"
		s, _, e := config.DecodeSettings(strings.NewReader(doc), dir)
		if e != nil || s.Repositories["app"].InlinePolicy == nil {
			t.Fatalf("inline policy: %v\n%s", e, doc)
		}
		return *s.Repositories["app"].InlinePolicy
	}
	for _, threshold := range []string{"3", "9"} {
		j := strings.Replace(richPolicyJSON, `"auto_approve_max":3`, `"auto_approve_max":`+threshold, 1)
		y := strings.Replace(richPolicyYAML, "auto_approve_max: 3", "auto_approve_max: "+threshold, 1)
		for name, body := range map[string]string{"p.json": j, "p.yaml": y, "p.yml": y} {
			if d, e := fileDigest(write(name, body)); e != nil || d != richPolicyDigest {
				t.Errorf("%s (auto_approve_max %s): digest %s %v, want %s", name, threshold, d, e, richPolicyDigest)
			}
		}
		if d := policyDigest(inline(y)); d != richPolicyDigest {
			t.Errorf("inline (auto_approve_max %s): digest %s, want %s", threshold, d, richPolicyDigest)
		}
		p, e := policy.Decode(strings.NewReader(j))
		if e != nil || policyDigest(p) != richPolicyDigest {
			t.Errorf("policyDigest(policy.Decode(JSON)) %s %v", policyDigest(p), e)
		}
	}
	// Any other change does change the digest.
	if d, _ := fileDigest(write("q.yaml", strings.Replace(richPolicyYAML, "minimum: 8", "minimum: 9", 1))); d == richPolicyDigest {
		t.Fatal("a rule change kept the digest")
	}
	if _, e := fileDigest(write("bad.yaml", richPolicyYAML+"base_minimum: 2\n")); e == nil {
		t.Fatal("duplicate key in a YAML policy accepted")
	}
}
