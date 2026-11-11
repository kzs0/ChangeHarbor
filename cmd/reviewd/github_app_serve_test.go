package main

import (
	"context"
	"testing"
)

// reviewd serve compiles repositories the same way as the CLI, so its forge
// clients authenticate with the App's installation token.
func TestServeUsesGitHubAppInstallationToken(t *testing.T) {
	a := newAppFixture(t, nil)
	a.resolve()
	t.Setenv("CH_WEBHOOK_SECRET", "webhook-secret")
	a.doc["service"] = map[string]any{"address": "127.0.0.1:0", "github_secret_env": "CH_WEBHOOK_SECRET"}
	a.write()
	c, e := loadServerConfig(a.path)
	if e != nil {
		t.Fatal(e)
	}
	repo, ok := c.Repositories["github:10"]
	if !ok || repo.forge == nil || repo.forge.appID != "123456" || repo.forge.installationID != "4242" || repo.forge.TokenEnv != "" {
		t.Fatalf("serve repository %+v", repo)
	}
	if _, e := githubClient(*repo.forge).Repository(context.Background(), "owner", "repo"); e != nil {
		t.Fatal(e)
	}
	issued, used, _, _, _ := a.forge.snapshot()
	if len(used) == 0 || used[len(used)-1] != issued[len(issued)-1] {
		t.Fatalf("serve client did not use an installation token: issued %v used %v", issued, used)
	}
}
