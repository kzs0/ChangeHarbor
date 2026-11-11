package git

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// HTTPAuth carries a forge token to git through the environment only: it
// never appears in argv, in the repository's config or anywhere on disk.
type HTTPAuth struct {
	URL      string // remote URL the header is scoped to; https or loopback http
	Username string // "x-access-token" (GitHub) or "oauth2" (GitLab)
	Token    string // empty: no header (unauthenticated)
}

func hasControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }

func (a HTTPAuth) validURL() error {
	raw := a.URL
	if raw == "" || strings.HasPrefix(raw, "-") || hasControl(raw) || strings.ContainsAny(raw, " \t") {
		return errors.New("unsafe remote URL")
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.Opaque != "" {
		return errors.New("remote URL must be an absolute https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("remote URL must not contain credentials, query or fragment")
	}
	host := u.Hostname()
	loopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		loopback = true
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return errors.New("remote URL must use https")
	}
	return nil
}

// Env returns the GIT_CONFIG_* entries that disable credential helpers and
// redirects and, with a token, add a Basic Authorization header scoped to
// exactly URL.
func (a HTTPAuth) Env() ([]string, error) {
	if e := a.validURL(); e != nil {
		return nil, e
	}
	if hasControl(a.Token) || hasControl(a.Username) || strings.Contains(a.Username, ":") {
		return nil, errors.New("unsafe git credential")
	}
	type kv struct{ k, v string }
	cfg := []kv{{"http.followRedirects", "false"}, {"credential.helper", ""}}
	if a.Token != "" {
		if a.Username == "" {
			return nil, errors.New("git credential username required")
		}
		basic := base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Token))
		cfg = append(cfg, kv{"http." + a.URL + ".extraHeader", "Authorization: Basic " + basic})
	}
	env := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(cfg))}
	for i, c := range cfg {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, c.k), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, c.v))
	}
	// No interactive or external credential prompts.
	env = append(env, "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_TERMINAL_PROMPT=0")
	return env, nil
}

func validRefspecs(refspecs []string) error {
	if len(refspecs) == 0 {
		return errors.New("unsafe fetch request")
	}
	for _, s := range refspecs {
		if !refspec.MatchString(s) || strings.Contains(s, "..") || strings.Contains(s, "//") {
			return errors.New("unsafe refspec")
		}
	}
	return nil
}

// FetchURL is Fetch from auth.URL instead of a configured remote name.
func (r Reader) FetchURL(ctx context.Context, auth HTTPAuth, refspecs ...string) error {
	if e := validRefspecs(refspecs); e != nil {
		return e
	}
	env, e := auth.Env()
	if e != nil {
		return e
	}
	args := append([]string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--update-head-ok", "--no-recurse-submodules", "--end-of-options", auth.URL}, refspecs...)
	_, e = r.runEnv(ctx, env, args...)
	return e
}

// CloneMirror creates dir with `git clone --mirror auth.URL dir`. dir must
// be missing or an empty directory; missing parents are created. The clone's
// origin URL is auth.URL, which carries no credentials.
func CloneMirror(ctx context.Context, auth HTTPAuth, dir string) error {
	env, e := auth.Env()
	if e != nil {
		return e
	}
	if dir == "" || strings.HasPrefix(dir, "-") || hasControl(dir) {
		return errors.New("unsafe mirror path")
	}
	dir, e = filepath.Abs(dir)
	if e != nil {
		return e
	}
	if entries, e := os.ReadDir(dir); e == nil {
		if len(entries) > 0 {
			return fmt.Errorf("mirror path %s exists and is not empty", dir)
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return fmt.Errorf("mirror path %s: %w", dir, e)
	}
	parent := filepath.Dir(dir)
	if e := os.MkdirAll(parent, 0o700); e != nil {
		return e
	}
	_, e = (Reader{Repo: parent}).runEnv(ctx, env, "clone", "--mirror", "--quiet", "--template=", "--no-local", "--end-of-options", auth.URL, dir)
	return e
}
