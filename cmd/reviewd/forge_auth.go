package main

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"

	gh "github.com/kzs0/reviewd/internal/forge/github"
	gitreader "github.com/kzs0/reviewd/internal/git"
)

// Forge credentials come only from named environment variables and live
// only in memory. Every GitHub API client is built by githubClient, so the
// CLI, publication, comments and `reviewd serve` authenticate the same way.

// githubClient returns the API client for a GitHub repository: a GitHub App
// installation token source, or the token_env token.
func githubClient(c forgeConfig) *gh.Client {
	if c.appID != "" {
		return gh.NewClientWithTokenSource(c.APIBase, appSource{c}, nil, 1<<20)
	}
	return gh.NewClient(c.APIBase, os.Getenv(c.TokenEnv), nil, 1<<20)
}

// loadAppKey reads and parses the App private key variable. Errors name the
// variable and never echo its value.
func loadAppKey(env string) (*rsa.PrivateKey, error) {
	raw := os.Getenv(env)
	if raw == "" {
		return nil, fmt.Errorf("GitHub App private key variable %s is not set", env)
	}
	key, e := gh.ParsePrivateKey([]byte(raw))
	if e != nil {
		return nil, fmt.Errorf("GitHub App private key variable %s: %w", env, e)
	}
	return key, nil
}

type appCacheKey struct{ base, appID, installation, repository, fingerprint string }

// appSources holds one installation token source per App installation and
// key for the life of the process: a long-running serve refreshes hourly and
// a CLI invocation exchanges at most once while its token is valid.
var (
	appSourcesMu sync.Mutex
	appSources   = map[appCacheKey]*gh.InstallationTokenSource{}
)

// appSource re-reads and parses the key on every use, so a removed or
// invalid key fails even while a token is cached.
type appSource struct{ c forgeConfig }

func (s appSource) Token(ctx context.Context) (string, error) {
	c := s.c
	if !numericID.MatchString(c.installationID) {
		return "", errors.New("GitHub App installation ID is not resolved; run reviewd config resolve")
	}
	key, e := loadAppKey(c.appKeyEnv)
	if e != nil {
		return "", e
	}
	der, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		return "", errors.New("GitHub App public key unavailable")
	}
	sum := sha256.Sum256(der)
	k := appCacheKey{c.APIBase, c.appID, c.installationID, c.Repository, hex.EncodeToString(sum[:])}
	appSourcesMu.Lock()
	src := appSources[k]
	if src == nil {
		src = (&gh.App{BaseURL: c.APIBase, AppID: c.appID, Key: key}).TokenSource(c.installationID, c.Repository)
		appSources[k] = src
	}
	appSourcesMu.Unlock()
	return src.Token(ctx)
}

// forgeToken is the repository's current forge token: the App installation
// token or the token_env value.
func forgeToken(ctx context.Context, c forgeConfig) (string, error) {
	if c.appID != "" {
		return appSource{c}.Token(ctx)
	}
	token := os.Getenv(c.TokenEnv)
	if c.TokenEnv != "" && token == "" {
		return "", fmt.Errorf("forge credential variable %s is not set", c.TokenEnv)
	}
	return token, nil
}

// mirrorHTTPAuth is how git authenticates to mirror.url: with the forge
// token (mirror.auth token) or with nothing.
func mirrorHTTPAuth(ctx context.Context, c forgeConfig) (gitreader.HTTPAuth, error) {
	auth := gitreader.HTTPAuth{URL: c.mirrorURL}
	if c.mirrorAuth != "token" {
		return auth, nil
	}
	token, e := forgeToken(ctx, c)
	if e != nil {
		return auth, e
	}
	if token == "" {
		return auth, errors.New("mirror.auth token requires a forge credential")
	}
	auth.Token, auth.Username = token, "x-access-token"
	if c.Provider == "gitlab" {
		auth.Username = "oauth2"
	}
	return auth, nil
}

// fetchMirror updates refspecs in the mirror, cloning it first when
// mirror.sync is auto and mirror.path doesn't exist. Without a sync source it
// is a no-op.
func fetchMirror(ctx context.Context, c forgeConfig, refspecs ...string) error {
	r := gitreader.Reader{Repo: c.RepoPath}
	if c.mirrorAuto {
		if _, e := os.Stat(c.RepoPath); errors.Is(e, os.ErrNotExist) {
			auth, e := mirrorHTTPAuth(ctx, c)
			if e != nil {
				return fmt.Errorf("mirror clone from %s: %w", c.mirrorURL, e)
			}
			if e := gitreader.CloneMirror(ctx, auth, c.RepoPath); e != nil {
				return fmt.Errorf("mirror clone from %s failed: %w", c.mirrorURL, e)
			}
		}
	}
	if c.mirrorAuth == "token" {
		auth, e := mirrorHTTPAuth(ctx, c)
		if e != nil {
			return fmt.Errorf("mirror sync from %s: %w", c.mirrorURL, e)
		}
		if e := r.FetchURL(ctx, auth, refspecs...); e != nil {
			return fmt.Errorf("mirror sync from %s failed: %w", c.mirrorURL, e)
		}
		return nil
	}
	remote := c.SyncRemote
	if remote == "" && c.mirrorAuto && r.HasRemote(ctx, "origin") {
		remote = "origin"
	}
	if remote == "" {
		return nil
	}
	if e := r.Fetch(ctx, remote, refspecs...); e != nil {
		return fmt.Errorf("mirror sync from %q failed: %w", remote, e)
	}
	return nil
}
