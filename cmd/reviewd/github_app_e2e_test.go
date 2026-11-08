package main

// End-to-end tests for GitHub App authentication and token-authenticated
// mirror sync (docs/design/github-app-and-model-providers.md, sections 1-2).
// A fake GitHub verifies RS256 App JWTs against the App's public key and
// issues installation tokens; a loopback smart-HTTP server (git http-backend
// via net/http/cgi) checks how git authenticates.

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kzs0/reviewd/internal/config"
	"go.yaml.in/yaml/v3"
)

const appPR = "https://github.com/owner/repo/pull/1"

type appKey struct {
	key *rsa.PrivateKey
	pem string
}

func newAppKey(t *testing.T) appKey {
	t.Helper()
	k, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	return appKey{k, string(b)}
}

// verifyAppJWT checks a GitHub App JWT: RS256 signed by the App key, iss is
// the App ID, iat backdated at most two minutes, lifetime at most ten.
func verifyAppJWT(token string, pub *rsa.PublicKey, appID string, now time.Time) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT")
	}
	hb, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	cb, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, e3 := base64.RawURLEncoding.DecodeString(parts[2])
	if e1 != nil || e2 != nil || e3 != nil {
		return errors.New("JWT is not base64url without padding")
	}
	var h struct{ Alg, Typ string }
	if json.Unmarshal(hb, &h) != nil || h.Alg != "RS256" {
		return fmt.Errorf("JWT header %s", hb)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if e := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); e != nil {
		return fmt.Errorf("JWT signature: %v", e)
	}
	var c struct {
		Iat, Exp int64
		Iss      json.RawMessage
	}
	if e := json.Unmarshal(cb, &c); e != nil {
		return e
	}
	if strings.Trim(string(c.Iss), `"`) != appID {
		return fmt.Errorf("iss %s, want %s", c.Iss, appID)
	}
	n := now.Unix()
	if c.Iat > n || c.Iat < n-120 || c.Exp <= n || c.Exp > n+600 || c.Exp-c.Iat > 600 {
		return fmt.Errorf("JWT times iat=%d exp=%d now=%d", c.Iat, c.Exp, n)
	}
	return nil
}

// appForge serves the GitHub App endpoints and requires an installation
// token (or the configured PAT) on every other request before delegating to
// the approval fake.
type appForge struct {
	t            *testing.T
	gh           *fakeGitHub
	pub          *rsa.PublicKey
	appID        string
	appIDReply   int
	installation int
	slug         string
	ttl          time.Duration
	latestOnly   bool   // reject any token but the most recently issued
	pat          string // accepted instead of an installation token when set

	mu                 sync.Mutex
	jwts, issued, used []string
	exchanges, lookups int
	paths              map[string]int
}

func (f *appForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths[r.Method+" "+r.URL.Path]++
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	jwt := func() bool {
		if e := verifyAppJWT(bearer, f.pub, f.appID, time.Now()); e != nil {
			f.t.Errorf("%s %s: invalid App JWT: %v", r.Method, r.URL.Path, e)
			w.WriteHeader(401)
			return false
		}
		f.jwts = append(f.jwts, bearer)
		return true
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/app":
		if jwt() {
			fmt.Fprintf(w, `{"id":%d,"slug":%q,"name":"Review App"}`, f.appIDReply, f.slug)
		}
		return
	case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/installation":
		if jwt() {
			f.lookups++
			fmt.Fprintf(w, `{"id":%d,"app_id":%s}`, f.installation, f.appID)
		}
		return
	case strings.HasPrefix(r.URL.Path, "/app/"):
		if !jwt() {
			return
		}
		if r.Method != "POST" || r.URL.Path != fmt.Sprintf("/app/installations/%d/access_tokens", f.installation) {
			f.t.Errorf("unexpected App request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		var in struct {
			Repositories []string `json:"repositories"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if !slices.Equal(in.Repositories, []string{"repo"}) {
			f.t.Errorf("installation token not scoped to the repository: %v", in.Repositories)
		}
		f.exchanges++
		tok := fmt.Sprintf("ghs_fixture%04d", len(f.issued)+1)
		f.issued = append(f.issued, tok)
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"token":%q,"expires_at":%q,"permissions":{"pull_requests":"write"}}`, tok, time.Now().Add(f.ttl).UTC().Format(time.RFC3339))
		return
	}
	ok := bearer != "" && (bearer == f.pat || slices.Contains(f.issued, bearer))
	if ok && f.latestOnly && bearer != f.pat && bearer != f.issued[len(f.issued)-1] {
		ok = false
	}
	if !ok {
		f.t.Errorf("%s %s: not authorised with a current installation token (Authorization %q)", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		w.WriteHeader(401)
		return
	}
	f.used = append(f.used, bearer)
	switch {
	case r.Method == "GET" && r.URL.Path == "/repos/owner/repo":
		fmt.Fprint(w, `{"id":10,"default_branch":"main","full_name":"owner/repo"}`)
	case r.Method == "GET" && r.URL.Path == "/users/"+f.slug+"[bot]":
		fmt.Fprintf(w, `{"id":9001,"login":%q,"type":"Bot"}`, f.slug+"[bot]")
	case r.URL.Path == "/user":
		f.t.Errorf("/user called; installation tokens cannot use it")
		w.WriteHeader(403)
	case f.gh != nil:
		f.gh.ServeHTTP(w, r)
	default:
		f.t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func (f *appForge) snapshot() (issued, used, jwts []string, exchanges, lookups int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.issued), slices.Clone(f.used), slices.Clone(f.jwts), f.exchanges, f.lookups
}

func (f *appForge) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paths[key]
}

func (f *appForge) isIssued(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.issued, token)
}

type appFixture struct {
	*gitlabFixture
	forge   *appForge
	gh      *fakeGitHub
	key     appKey
	path    string
	doc     map[string]any
	outputs []string
}

// newAppFixture writes a GitHub repository authenticated as an App (or with
// the given github.app settings) against the fake forge.
func newAppFixture(t *testing.T, app map[string]any) *appFixture {
	x := newGitLabFixture(t)
	key := newAppKey(t)
	gh := &fakeGitHub{t: t, head: x.head, base: x.base, state: "open", checks: `{"total_count":1,"check_runs":[{"name":"test","head_sha":"HEAD","conclusion":"success","app":{"id":42}}]}`,
		protection: `{"required_status_checks":{"strict":true,"contexts":["test"]}}`}
	f := &appForge{t: t, gh: gh, pub: &key.key.PublicKey, appID: "123456", appIDReply: 123456, installation: 4242, slug: "review-app", ttl: time.Hour, paths: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("CH_APP_KEY", key.pem)
	if app == nil {
		app = map[string]any{"app_id": 123456, "private_key_env": "CH_APP_KEY"}
	}
	response, _ := filepath.Abs("../../testdata/review-response.json")
	repo := map[string]any{"url": "https://github.com/owner/repo", "api_url": srv.URL, "mirror": map[string]any{"path": x.repo, "sync": "none"},
		"policy": x.policy, "response": response, "publish": map[string]any{"approvals": "submit"},
		"github": map[string]any{"required_checks": map[string]any{"test": "42"}, "app": app}}
	a := &appFixture{gitlabFixture: x, forge: f, gh: gh, key: key, path: filepath.Join(x.dir, "changeharbor.yaml"),
		doc: map[string]any{"version": 1, "state": x.state, "repositories": map[string]any{"svc": repo}}}
	a.write()
	return a
}

func (a *appFixture) repo() map[string]any {
	return a.doc["repositories"].(map[string]any)["svc"].(map[string]any)
}
func (a *appFixture) write() {
	b, e := yaml.Marshal(a.doc)
	if e != nil {
		a.t.Fatal(e)
	}
	if e := os.WriteFile(a.path, b, 0600); e != nil {
		a.t.Fatal(e)
	}
}
func (a *appFixture) cli(args ...string) (int, string, string) {
	code, out, stderr := runCLI(a.t, append(args, "--config", a.path)...)
	a.outputs = append(a.outputs, out, stderr)
	return code, out, stderr
}
func (a *appFixture) resolve() {
	a.t.Helper()
	if code, out, stderr := a.cli("config", "resolve"); code != 0 {
		a.t.Fatalf("resolve %d %s %s", code, out, stderr)
	}
}
func (a *appFixture) review() string {
	a.t.Helper()
	code, out, stderr := a.cli("review", "--url", appPR)
	m := savedRun.FindStringSubmatch(stderr)
	if code != 0 || m == nil {
		a.t.Fatalf("review %d %s %s", code, out, stderr)
	}
	return m[1]
}
func (a *appFixture) lock() config.LockedRepository {
	a.t.Helper()
	l, e := config.LoadLock(config.LockPath(a.path))
	if e != nil {
		a.t.Fatal(e)
	}
	return l.Repositories["svc"]
}
func (a *appFixture) check() checkOut {
	a.t.Helper()
	code, out, stderr := a.cli("config", "check")
	var c checkOut
	if e := json.Unmarshal([]byte(out), &c); code != 0 || e != nil {
		a.t.Fatalf("config check %d %v %s %s", code, e, out, stderr)
	}
	return c
}

// assertNoSecrets fails if a token, JWT or the private key reached any
// command output or any file under the given directories.
func (a *appFixture) assertNoSecrets(dirs ...string) {
	a.t.Helper()
	issued, _, jwts, _, _ := a.forge.snapshot()
	secrets := append(append([]string{}, issued...), jwts...)
	for _, tok := range issued {
		secrets = append(secrets, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok)))
	}
	lines := strings.Split(strings.TrimSpace(a.key.pem), "\n")
	secrets = append(secrets, lines[1], lines[len(lines)/2])
	assertNoSecretsIn(a.t, secrets, a.outputs, append(dirs, a.state, config.LockPath(a.path))...)
}

func assertNoSecretsIn(t *testing.T, secrets, outputs []string, paths ...string) {
	t.Helper()
	for _, s := range secrets {
		for _, out := range outputs {
			if strings.Contains(out, s) {
				t.Fatalf("secret %.12s... appeared in command output", s)
			}
		}
	}
	for _, root := range paths {
		filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			b, _ := os.ReadFile(p)
			for _, s := range secrets {
				if strings.Contains(string(b), s) {
					t.Fatalf("secret %.12s... written to %s", s, p)
				}
			}
			return nil
		})
	}
}

func TestGitHubAppResolveRecordsInstallationAndBot(t *testing.T) {
	a := newAppFixture(t, nil)
	a.resolve()
	l := a.lock()
	if l.RepositoryID != "10" || l.SourceRepositoryID != "10" || l.DefaultBranch != "main" || l.InstallationID != "4242" || l.AppSlug != "review-app" || l.BotUserID != "9001" {
		t.Fatalf("lock %+v", l)
	}
	if l.Inputs.GitHubAppID != "123456" || l.Inputs.GitHubAppPrivateKeyEnv != "CH_APP_KEY" || l.Inputs.GitHubAppInstallationID != "" || l.Inputs.TokenEnv != "" {
		t.Fatalf("lock inputs %+v", l.Inputs)
	}
	_, used, jwts, _, lookups := a.forge.snapshot()
	if lookups != 1 || len(jwts) == 0 || len(used) == 0 || a.forge.count("GET /app") == 0 || a.forge.count("GET /users/review-app[bot]") != 1 || a.forge.count("GET /user") != 0 {
		t.Fatalf("resolve requests: /app %d /users %d /user %d lookups %d", a.forge.count("GET /app"), a.forge.count("GET /users/review-app[bot]"), a.forge.count("GET /user"), lookups)
	}
	c := a.check().Repositories["svc"]
	if c.Effective["auth"] != "github_app" || c.Effective["github_app_id"] != "123456" || c.Effective["github_installation_id"] != "4242" || c.Sources["github_installation_id"] != "lock" || c.Effective["bot_user_id"] != "9001" {
		t.Fatalf("effective %v sources %v", c.Effective, c.Sources)
	}
	for _, k := range c.InvalidatesReviews {
		if strings.HasPrefix(k, "github.app") || k == "token_env" {
			t.Fatalf("authentication setting %s invalidates reviews", k)
		}
	}
	a.assertNoSecrets()
}

func TestGitHubAppResolveUsesConfiguredInstallation(t *testing.T) {
	a := newAppFixture(t, map[string]any{"app_id": 123456, "private_key_env": "CH_APP_KEY", "installation_id": 4242})
	a.resolve()
	if l := a.lock(); l.InstallationID != "4242" || l.Inputs.GitHubAppInstallationID != "4242" || l.BotUserID != "9001" {
		t.Fatalf("lock %+v", l)
	}
	if _, _, _, _, lookups := a.forge.snapshot(); lookups != 0 {
		t.Fatalf("looked up the installation although it is configured (%d)", lookups)
	}
	if c := a.check().Repositories["svc"]; c.Sources["github_installation_id"] != "file" {
		t.Fatalf("sources %v", c.Sources)
	}
	// Changing the App settings makes the lock stale.
	a.repo()["github"].(map[string]any)["app"].(map[string]any)["app_id"] = 654321
	a.write()
	if code, _, stderr := a.cli("review", "--url", appPR); code != 2 || !strings.Contains(stderr, "stale") || !strings.Contains(stderr, "github_app_id") {
		t.Fatalf("stale App lock accepted: %d %s", code, stderr)
	}
}

func TestGitHubAppResolveRejectsMismatchedAppID(t *testing.T) {
	a := newAppFixture(t, nil)
	a.forge.appIDReply = 999
	if code, _, stderr := a.cli("config", "resolve"); code == 0 || !strings.Contains(strings.ToLower(stderr), "app id") {
		t.Fatalf("mismatched App accepted: %d %s", code, stderr)
	}
}

func TestGitHubAppReviewPublishRevalidateUseInstallationToken(t *testing.T) {
	a := newAppFixture(t, nil)
	a.resolve()
	_, _, _, exchanges, lookups := a.forge.snapshot()
	run := a.review()
	code, out, stderr := a.cli("publish", "--run", run)
	if p := decode(t, out); code != 0 || p.State != "published" || len(a.gh.approvals) != 1 || a.gh.approvals[0] != a.head {
		t.Fatalf("publish %d %+v %s approvals %v", code, p, stderr, a.gh.approvals)
	}
	if code, _, stderr := a.cli("revalidate"); code != 0 {
		t.Fatalf("revalidate %d %s", code, stderr)
	}
	issued, used, _, exchanges2, lookups2 := a.forge.snapshot()
	if lookups2 != lookups {
		t.Fatal("runtime looked up the installation instead of using the lock")
	}
	if exchanges2-exchanges > 1 {
		t.Fatalf("exchanged %d tokens while the first was valid for an hour", exchanges2-exchanges)
	}
	if len(used) < 4 {
		t.Fatalf("expected the forge calls to be authenticated, saw %d", len(used))
	}
	for _, tok := range used {
		if !slices.Contains(issued, tok) {
			t.Fatalf("API call used %q, not an installation token", tok)
		}
	}
	a.assertNoSecrets(a.repo()["mirror"].(map[string]any)["path"].(string))
}

func TestGitHubAppRefreshesExpiringToken(t *testing.T) {
	a := newAppFixture(t, nil)
	// Tokens live two minutes, inside the five-minute refresh margin, and the
	// fake accepts only the newest one: every call must refresh first.
	a.forge.ttl, a.forge.latestOnly = 2*time.Minute, true
	a.resolve()
	run := a.review()
	code, out, stderr := a.cli("publish", "--run", run)
	if p := decode(t, out); code != 0 || p.State != "published" {
		t.Fatalf("publish %d %+v %s", code, p, stderr)
	}
	issued, used, _, _, _ := a.forge.snapshot()
	distinct := map[string]bool{}
	for _, tok := range used {
		distinct[tok] = true
	}
	if len(distinct) < 2 || used[len(used)-1] != issued[len(issued)-1] {
		t.Fatalf("expiring token not refreshed: issued %v used %v", issued, used)
	}
}

func TestGitHubAppMissingOrInvalidKeyFails(t *testing.T) {
	a := newAppFixture(t, nil)
	a.resolve()
	t.Setenv("CH_APP_KEY", "")
	if code, _, stderr := a.cli("review", "--url", appPR); code == 0 || !strings.Contains(stderr, "CH_APP_KEY") {
		t.Fatalf("missing key accepted: %d %s", code, stderr)
	}
	t.Setenv("CH_APP_KEY", "-----BEGIN RSA PRIVATE KEY-----\nQUFBQUFBQUFBQUFB\n-----END RSA PRIVATE KEY-----\n")
	if code, _, stderr := a.cli("review", "--url", appPR); code == 0 || !strings.Contains(stderr, "CH_APP_KEY") || strings.Contains(stderr, "QUFBQUFB") {
		t.Fatalf("invalid key accepted or echoed: %d %s", code, stderr)
	}
}

func TestGitHubAppAndTokenEnvAreExclusive(t *testing.T) {
	a := newAppFixture(t, nil)
	a.repo()["token_env"] = "GL_TOKEN"
	a.write()
	code, _, stderr := a.cli("config", "resolve")
	if code == 0 || !strings.Contains(stderr, "token_env") || !strings.Contains(stderr, "github.app") {
		t.Fatalf("token_env with github.app accepted: %d %s", code, stderr)
	}
}

// Authentication is not review identity: a review made with a PAT stays
// publishable after the repository moves to an App.
func TestSwitchingToGitHubAppKeepsReviewsValid(t *testing.T) {
	a := newAppFixture(t, nil)
	t.Setenv("GH_PAT", "ghp_fixture")
	a.forge.pat = "ghp_fixture"
	app := a.repo()["github"].(map[string]any)["app"]
	delete(a.repo()["github"].(map[string]any), "app")
	a.repo()["token_env"], a.repo()["bot_user_id"] = "GH_PAT", "9001"
	a.write()
	a.resolve()
	run := a.review()
	a.repo()["github"].(map[string]any)["app"] = app
	delete(a.repo(), "token_env")
	delete(a.repo(), "bot_user_id")
	a.write()
	a.resolve()
	code, out, stderr := a.cli("publish", "--run", run)
	if p := decode(t, out); code != 0 || p.State != "published" {
		t.Fatalf("switching auth invalidated the review: %d %+v %s", code, p, stderr)
	}
	_, used, _, _, _ := a.forge.snapshot()
	if last := used[len(used)-1]; !strings.HasPrefix(last, "ghs_") {
		t.Fatalf("publication after the switch used %q", last)
	}
}

// --- Token-authenticated mirrors over loopback smart HTTP ---

type smartHTTP struct {
	srv   *httptest.Server
	user  string
	valid func(password string) bool // nil: no authentication required

	mu                            sync.Mutex
	requests, rejected, anonymous int
}

func gitHTTPBackend(t *testing.T) string {
	t.Helper()
	out, e := exec.Command("git", "--exec-path").Output()
	if e != nil {
		t.Skipf("git --exec-path: %v", e)
	}
	p := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, e := os.Stat(p); e != nil {
		t.Skipf("git http-backend unavailable: %v", e)
	}
	return p
}

// newSmartHTTP serves the bare repositories under root.
func newSmartHTTP(t *testing.T, root, user string, valid func(string) bool) *smartHTTP {
	backend := gitHTTPBackend(t)
	s := &smartHTTP{user: user, valid: valid}
	h := &cgi.Handler{Path: backend, Root: "/", Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		ok := s.valid == nil
		if raw, found := strings.CutPrefix(auth, "Basic "); found && !ok {
			b, e := base64.StdEncoding.DecodeString(raw)
			u, p, _ := strings.Cut(string(b), ":")
			ok = e == nil && u == s.user && s.valid(p)
		}
		s.mu.Lock()
		s.requests++
		if auth == "" {
			s.anonymous++
		}
		if !ok {
			s.rejected++
		}
		s.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(401)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *smartHTTP) counts() (requests, rejected, anonymous int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.rejected, s.anonymous
}

// bareUpstream publishes src as root/<name> with ref pointing at head.
func bareUpstream(t *testing.T, src, root, name, ref, head string) {
	upstream{t, root}.git("clone", "--bare", src, name)
	upstream{t, filepath.Join(root, name)}.git("update-ref", ref, head)
}

func TestGitHubAppMirrorCloneAndFetchUseHeaderAuth(t *testing.T) {
	a := newAppFixture(t, nil)
	root := t.TempDir()
	bareUpstream(t, a.gitlabFixture.repo, root, "owner/repo.git", "refs/pull/1/head", a.head)
	s := newSmartHTTP(t, root, "x-access-token", a.forge.isIssued)
	mirror := filepath.Join(a.dir, "mirrors", "repo.git")
	// mirror.auth defaults to token for an App with mirror.sync auto.
	a.repo()["mirror"] = map[string]any{"path": mirror, "url": s.srv.URL + "/owner/repo.git"}
	a.write()
	a.resolve()
	if c := a.check().Repositories["svc"]; c.Effective["mirror_auth"] != "token" {
		t.Fatalf("mirror auth %v", c.Effective)
	}
	a.review()
	m := upstream{t, mirror}
	if got := m.git("rev-parse", "refs/pull/1/head"); got != a.head {
		t.Fatalf("PR head not fetched into the new mirror: %s", got)
	}
	if got := m.git("rev-parse", "refs/heads/main"); got != a.base {
		t.Fatalf("target branch not in the new mirror: %s", got)
	}
	if got := m.git("config", "--get", "remote.origin.url"); got != s.srv.URL+"/owner/repo.git" {
		t.Fatalf("mirror origin %q", got)
	}
	requests, rejected, anonymous := s.counts()
	if requests == 0 || rejected != 0 || anonymous != 0 {
		t.Fatalf("git authentication: requests %d rejected %d anonymous %d", requests, rejected, anonymous)
	}
	a.review() // the existing mirror is fetched with a header too
	if r2, rejected, _ := s.counts(); r2 == requests || rejected != 0 {
		t.Fatalf("second sync: requests %d->%d rejected %d", requests, r2, rejected)
	}
	a.assertNoSecrets(mirror)
}

func TestGitLabMirrorTokenAuthIsOptIn(t *testing.T) {
	u := newUnified(t, nil)
	root := t.TempDir()
	bareUpstream(t, u.gitlabFixture.repo, root, "group/app.git", "refs/merge-requests/7/head", u.head)
	s := newSmartHTTP(t, root, "oauth2", func(p string) bool { return p == "glpat-fixture" })
	mirror := filepath.Join(u.dir, "mirrors", "app.git")
	u.repo()["mirror"] = map[string]any{"path": mirror, "auth": "token", "url": s.srv.URL + "/group/app.git"}
	u.write()
	u.resolve()
	u.review()
	if got := (upstream{t, mirror}).git("rev-parse", "refs/merge-requests/7/head"); got != u.head {
		t.Fatalf("MR head not fetched: %s", got)
	}
	if requests, rejected, anonymous := s.counts(); requests == 0 || rejected != 0 || anonymous != 0 {
		t.Fatalf("git authentication: requests %d rejected %d anonymous %d", requests, rejected, anonymous)
	}
	secret := base64.StdEncoding.EncodeToString([]byte("oauth2:glpat-fixture"))
	assertNoSecretsIn(t, []string{"glpat-fixture", secret}, nil, mirror, u.state)
}

func TestMirrorWithoutTokenAuthSendsNoCredential(t *testing.T) {
	u := newUnified(t, nil)
	root := t.TempDir()
	bareUpstream(t, u.gitlabFixture.repo, root, "group/app.git", "refs/merge-requests/7/head", u.head)
	s := newSmartHTTP(t, root, "oauth2", func(p string) bool { return p == "glpat-fixture" })
	u.repo()["mirror"] = map[string]any{"path": filepath.Join(u.dir, "mirrors", "app.git"), "url": s.srv.URL + "/group/app.git"}
	u.write()
	u.resolve()
	code, _, stderr := runCLI(t, "review", "--url", mrURL, "--config", u.path)
	if code == 0 || !strings.Contains(stderr, "mirror") {
		t.Fatalf("clone without credentials succeeded or error unclear: %d %s", code, stderr)
	}
	if requests, _, anonymous := s.counts(); requests == 0 || anonymous != requests {
		t.Fatalf("a credential was sent although mirror.auth is none: requests %d anonymous %d", requests, anonymous)
	}
}
