package github

import (
	"bytes"
	"context"
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
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// TokenSource supplies the bearer token for each API request.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// NewClientWithTokenSource is NewClient whose every request asks ts for the
// current token. A token-source error fails the request before it is sent.
func NewClientWithTokenSource(baseURL string, ts TokenSource, httpClient *http.Client, maxBytes int64) *Client {
	c := NewClient(baseURL, "", httpClient, maxBytes)
	c.ts = ts
	return c
}

// authorization returns the Authorization header value, or "" for none.
func (c *Client) authorization(ctx context.Context) (string, error) {
	if c.ts != nil {
		tok, e := c.ts.Token(ctx)
		if e != nil {
			return "", fmt.Errorf("GitHub credential unavailable: %w", e)
		}
		if !validToken(tok) {
			return "", errors.New("GitHub credential unavailable: invalid token")
		}
		return "Bearer " + tok, nil
	}
	if c.token != "" {
		return "Bearer " + c.token, nil
	}
	return "", nil
}

func validToken(t string) bool {
	return t != "" && strings.IndexFunc(t, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}

// TokenRefreshMargin is how long before expiry a cached installation token
// is replaced.
const TokenRefreshMargin = 5 * time.Minute

// ParsePrivateKey parses a GitHub App private key: exactly one unencrypted
// PEM block holding an RSA key (PKCS#1 or PKCS#8) of at least 2048 bits.
// Errors never include key material.
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	in := bytes.TrimSpace(pemBytes)
	if !bytes.HasPrefix(in, []byte("-----BEGIN ")) {
		return nil, errors.New("private key is not PEM encoded")
	}
	block, rest := pem.Decode(in)
	if block == nil {
		return nil, errors.New("private key is not PEM encoded")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("private key has trailing data")
	}
	if len(block.Headers) != 0 {
		return nil, errors.New("encrypted private keys are not supported")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, e := x509.ParsePKCS1PrivateKey(block.Bytes)
		if e != nil {
			return nil, errors.New("invalid PKCS#1 RSA private key")
		}
		key = k
	case "PRIVATE KEY":
		k, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		if e != nil {
			return nil, errors.New("invalid PKCS#8 private key")
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not an RSA key")
		}
		key = rk
	default:
		return nil, errors.New("PEM block is not a private key")
	}
	if key.N.BitLen() < 2048 {
		return nil, errors.New("RSA private key is shorter than 2048 bits")
	}
	return key, nil
}

var numeric = regexp.MustCompile(`^[1-9][0-9]*$`)

// SignAppJWT returns a GitHub App JWT (RS256) issued by appID, valid from a
// minute before now for ten minutes.
func SignAppJWT(key *rsa.PrivateKey, appID string, now time.Time) (string, error) {
	if key == nil {
		return "", errors.New("GitHub App private key required")
	}
	if !numeric.MatchString(appID) {
		return "", errors.New("GitHub App ID must be numeric")
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, e := json.Marshal(struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}{now.Unix() - 60, now.Unix() + 540, appID})
	if e != nil {
		return "", e
	}
	signed := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if e != nil {
		return "", errors.New("signing GitHub App JWT failed")
	}
	return signed + "." + enc.EncodeToString(sig), nil
}

// App authenticates as a GitHub App. Endpoints are relative to BaseURL, so
// GitHub Enterprise Server works unchanged.
type App struct {
	BaseURL  string           // API base, e.g. https://api.github.com
	AppID    string           // numeric App ID
	Key      *rsa.PrivateKey  // the App's private key
	HTTP     *http.Client     // nil: 30 s timeout client
	Now      func() time.Time // nil: time.Now
	MaxBytes int64            // <= 0: 1 MiB
}

// AppIdentity is the App as GET /app reports it.
type AppIdentity struct{ ID, Slug string }

// InstallationToken is a short-lived installation access token.
type InstallationToken struct {
	Token     string
	ExpiresAt time.Time
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// do sends one JWT-authenticated request and decodes a want-status reply.
// Redirects are never followed and errors never contain the JWT.
func (a *App) do(ctx context.Context, method, path string, input any, want int, out any) error {
	base := strings.TrimRight(a.BaseURL, "/")
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("invalid API base URL")
	}
	jwt, e := SignAppJWT(a.Key, a.AppID, a.now())
	if e != nil {
		return e
	}
	var body io.Reader
	if input != nil {
		b, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, base+path, body)
	if e != nil {
		return errors.New("invalid GitHub App request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwt)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	if a.HTTP != nil {
		*hc = *a.HTTP
		if hc.Timeout <= 0 {
			hc.Timeout = 30 * time.Second
		}
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, e := hc.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("GitHub App request %s %s failed", method, path)
	}
	defer res.Body.Close()
	if res.StatusCode != want {
		return fmt.Errorf("GitHub App request %s %s: HTTP status %d", method, path, res.StatusCode)
	}
	max := a.MaxBytes
	if max <= 0 {
		max = 1 << 20
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, max+1))
	if e != nil {
		return fmt.Errorf("GitHub App request %s %s: reading response failed", method, path)
	}
	if int64(len(b)) > max {
		return fmt.Errorf("GitHub App request %s %s: response exceeds byte cap", method, path)
	}
	if json.Unmarshal(b, out) != nil {
		return fmt.Errorf("GitHub App request %s %s: invalid response JSON", method, path)
	}
	return nil
}

// Identity returns the App behind the key; its ID must equal AppID.
func (a *App) Identity(ctx context.Context) (AppIdentity, error) {
	var x struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if e := a.do(ctx, "GET", "/app", nil, http.StatusOK, &x); e != nil {
		return AppIdentity{}, e
	}
	if strconv.FormatInt(x.ID, 10) != a.AppID {
		return AppIdentity{}, fmt.Errorf("GitHub App ID mismatch: the key belongs to App %d, configured app_id is %s", x.ID, a.AppID)
	}
	if !slugPattern.MatchString(x.Slug) {
		return AppIdentity{}, errors.New("GitHub App response has an invalid slug")
	}
	return AppIdentity{ID: a.AppID, Slug: x.Slug}, nil
}

var slugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// Installation returns the App's installation ID for a repository.
func (a *App) Installation(ctx context.Context, owner, repo string) (string, error) {
	p, e := repoPath(owner, repo)
	if e != nil {
		return "", e
	}
	var x struct {
		ID int64 `json:"id"`
	}
	if e := a.do(ctx, "GET", p+"/installation", nil, http.StatusOK, &x); e != nil {
		return "", e
	}
	if x.ID <= 0 {
		return "", errors.New("GitHub App installation response has no ID")
	}
	return strconv.FormatInt(x.ID, 10), nil
}

// InstallationToken exchanges the App JWT for an installation token scoped
// to the given repositories (names within the installation's owner).
func (a *App) InstallationToken(ctx context.Context, installationID string, repositories ...string) (InstallationToken, error) {
	if !numeric.MatchString(installationID) {
		return InstallationToken{}, errors.New("GitHub App installation ID must be numeric")
	}
	for _, r := range repositories {
		if !segment.MatchString(r) || r == "." || r == ".." {
			return InstallationToken{}, errors.New("invalid repository name")
		}
	}
	var input any
	if len(repositories) > 0 {
		input = map[string][]string{"repositories": repositories}
	} else {
		input = map[string]any{}
	}
	var x struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if e := a.do(ctx, "POST", "/app/installations/"+installationID+"/access_tokens", input, http.StatusCreated, &x); e != nil {
		return InstallationToken{}, e
	}
	if !validToken(x.Token) {
		return InstallationToken{}, errors.New("GitHub App installation token response has no valid token")
	}
	exp, e := time.Parse(time.RFC3339, x.ExpiresAt)
	if e != nil {
		return InstallationToken{}, errors.New("GitHub App installation token response has no valid expires_at")
	}
	if !exp.After(a.now()) {
		return InstallationToken{}, errors.New("GitHub App installation token is already expired")
	}
	return InstallationToken{Token: x.Token, ExpiresAt: exp}, nil
}

// TokenSource returns a cached, refreshing installation token source.
func (a *App) TokenSource(installationID string, repositories ...string) *InstallationTokenSource {
	return &InstallationTokenSource{app: a, installation: installationID, repositories: append([]string(nil), repositories...)}
}

// InstallationTokenSource caches an installation token in memory and
// replaces it when it is within TokenRefreshMargin of expiry. It is safe for
// concurrent use.
type InstallationTokenSource struct {
	app          *App
	installation string
	repositories []string

	mu     sync.Mutex
	cached InstallationToken
}

// Token returns a current installation token, exchanging a new one when the
// cached token is missing or about to expire. A freshly issued token is
// returned even when it is already inside the margin.
func (s *InstallationTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached.Token != "" && s.cached.ExpiresAt.Sub(s.app.now()) >= TokenRefreshMargin {
		return s.cached.Token, nil
	}
	s.cached = InstallationToken{}
	t, e := s.app.InstallationToken(ctx, s.installation, s.repositories...)
	if e != nil {
		return "", e
	}
	s.cached = t
	return t.Token, nil
}

// BotLogin is the login of a GitHub App's bot user.
func BotLogin(slug string) string { return slug + "[bot]" }

var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*(\[bot\])?$`)

// UserID returns the numeric ID of a user (or App bot) by login.
func (c *Client) UserID(ctx context.Context, login string) (string, error) {
	if !loginPattern.MatchString(login) {
		return "", errors.New("invalid GitHub login")
	}
	var x struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if e := c.request(ctx, "GET", "/users/"+url.PathEscape(login), nil, &x); e != nil {
		return "", e
	}
	if x.ID <= 0 || (x.Login != "" && !strings.EqualFold(x.Login, login)) {
		return "", errors.New("GitHub user identity mismatch")
	}
	return strconv.FormatInt(x.ID, 10), nil
}
