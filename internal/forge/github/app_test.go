package github

// Unit tests for GitHub App authentication
// (docs/design/github-app-and-model-providers.md, section 1).

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	appKeyOnce sync.Once
	appKeyVal  *rsa.PrivateKey
)

func appTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	appKeyOnce.Do(func() {
		k, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			panic(e)
		}
		appKeyVal = k
	})
	return appKeyVal
}

func appJWTParts(t *testing.T, token string) (header, claims map[string]any, signed string, sig []byte) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a compact JWT: %q", token)
	}
	for _, p := range parts {
		if strings.ContainsAny(p, "=+/") {
			t.Fatalf("JWT segment is not unpadded base64url: %q", p)
		}
	}
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, _ = base64.RawURLEncoding.DecodeString(parts[2])
	if e := json.Unmarshal(hb, &header); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal(cb, &claims); e != nil {
		t.Fatal(e)
	}
	return header, claims, parts[0] + "." + parts[1], sig
}

func TestParsePrivateKeyAcceptsPKCS1AndPKCS8(t *testing.T) {
	k := appTestKey(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	der8, e := x509.MarshalPKCS8PrivateKey(k)
	if e != nil {
		t.Fatal(e)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der8})
	for name, b := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8, "pkcs1 with surrounding whitespace": append(append([]byte("\n"), pkcs1...), '\n')} {
		got, e := ParsePrivateKey(b)
		if e != nil || !got.Equal(k) {
			t.Fatalf("%s: %v", name, e)
		}
	}
}

func TestParsePrivateKeyRejectsOtherInput(t *testing.T) {
	k := appTestKey(t)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKCS8PrivateKey(ec)
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	encrypted := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00000000000000000000000000000000"}, Bytes: []byte("ciphertext")})
	for name, b := range map[string][]byte{
		"empty":         nil,
		"garbage":       []byte("not a key"),
		"bad DER":       pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("QUFBQUFB")}),
		"ec key":        pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}),
		"public key":    pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&k.PublicKey)}),
		"1024-bit key":  pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(small)}),
		"encrypted PEM": encrypted,
		"trailing data": append(append([]byte{}, pkcs1...), []byte("extra")...),
		"two keys":      append(append([]byte{}, pkcs1...), pkcs1...),
	} {
		if _, e := ParsePrivateKey(b); e == nil {
			t.Errorf("%s accepted", name)
		} else if strings.Contains(e.Error(), "QUFB") || strings.Contains(e.Error(), "BEGIN") {
			t.Errorf("%s: error echoes key material: %v", name, e)
		}
	}
}

func TestSignAppJWT(t *testing.T) {
	k := appTestKey(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tok, e := SignAppJWT(k, "123456", now)
	if e != nil {
		t.Fatal(e)
	}
	h, c, signed, sig := appJWTParts(t, tok)
	if h["alg"] != "RS256" || h["typ"] != "JWT" {
		t.Fatalf("header %v", h)
	}
	if c["iss"] != "123456" || c["iat"] != float64(now.Unix()-60) || c["exp"] != float64(now.Unix()+540) {
		t.Fatalf("claims %v", c)
	}
	sum := sha256.Sum256([]byte(signed))
	if e := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], sig); e != nil {
		t.Fatalf("signature does not verify with the App public key: %v", e)
	}
	for _, id := range []string{"", "abc", "12 3", "-1", "0"} {
		if _, e := SignAppJWT(k, id, now); e == nil {
			t.Errorf("app ID %q accepted", id)
		}
	}
	if _, e := SignAppJWT(nil, "1", now); e == nil {
		t.Error("nil key accepted")
	}
}

// fakeAppAPI serves the App endpoints and records what it saw.
type fakeAppAPI struct {
	t        *testing.T
	now      func() time.Time
	ttl      time.Duration
	status   int    // non-zero: access_tokens answers with this status
	body     string // non-empty: access_tokens answers with this body
	mu       sync.Mutex
	issued   []string
	requests []*http.Request
	bodies   []string
}

func (f *fakeAppAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, string(b))
	switch {
	case r.Method == "GET" && r.URL.Path == "/app":
		fmt.Fprint(w, `{"id":123456,"slug":"review-app"}`)
	case r.Method == "GET" && r.URL.Path == "/repos/acme/app/installation":
		fmt.Fprint(w, `{"id":4242}`)
	case r.Method == "POST" && r.URL.Path == "/app/installations/4242/access_tokens":
		if f.status != 0 {
			w.WriteHeader(f.status)
			fmt.Fprint(w, `{"message":"nope"}`)
			return
		}
		if f.body != "" {
			w.WriteHeader(201)
			fmt.Fprint(w, f.body)
			return
		}
		tok := fmt.Sprintf("ghs_unit%03d", len(f.issued)+1)
		f.issued = append(f.issued, tok)
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, tok, f.now().Add(f.ttl).UTC().Format(time.RFC3339))
	case r.Method == "GET" && r.URL.Path == "/users/review-app[bot]":
		fmt.Fprint(w, `{"id":9001,"login":"review-app[bot]"}`)
	case r.Method == "GET" && r.URL.Path == "/repos/acme/app":
		fmt.Fprint(w, `{"id":10,"default_branch":"main","full_name":"acme/app"}`)
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeAppAPI) exchanges() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			n++
		}
	}
	return n
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newAppUnderTest(t *testing.T, f *fakeAppAPI) (*App, *httptest.Server) {
	clock := &fakeClock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	if f.now == nil {
		f.now = clock.now
	}
	if f.ttl == 0 {
		f.ttl = time.Hour
	}
	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &App{BaseURL: srv.URL, AppID: "123456", Key: appTestKey(t), Now: f.now}, srv
}

func TestAppIdentityAndInstallationUseJWT(t *testing.T) {
	f := &fakeAppAPI{}
	a, _ := newAppUnderTest(t, f)
	id, e := a.Identity(context.Background())
	if e != nil || id.ID != "123456" || id.Slug != "review-app" {
		t.Fatalf("identity %+v %v", id, e)
	}
	inst, e := a.Installation(context.Background(), "acme", "app")
	if e != nil || inst != "4242" {
		t.Fatalf("installation %q %v", inst, e)
	}
	for _, r := range f.requests {
		bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			t.Fatalf("%s without bearer JWT", r.URL.Path)
		}
		_, c, _, _ := appJWTParts(t, bearer)
		if c["iss"] != "123456" || c["iat"] != float64(a.Now().Unix()-60) {
			t.Fatalf("%s: claims %v (clock not injected?)", r.URL.Path, c)
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("%s: Accept %q", r.URL.Path, r.Header.Get("Accept"))
		}
	}
	a.AppID = "999"
	if _, e := a.Identity(context.Background()); e == nil {
		t.Fatal("GET /app for another App accepted")
	}
	if _, e := a.Installation(context.Background(), "../x", "app"); e == nil {
		t.Fatal("unsafe owner accepted")
	}
}

func TestInstallationTokenExchange(t *testing.T) {
	f := &fakeAppAPI{}
	a, _ := newAppUnderTest(t, f)
	tok, e := a.InstallationToken(context.Background(), "4242", "app")
	if e != nil || tok.Token != "ghs_unit001" || !tok.ExpiresAt.Equal(a.Now().Add(time.Hour)) {
		t.Fatalf("token %+v %v", tok, e)
	}
	r := f.requests[len(f.requests)-1]
	if r.Method != "POST" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
		t.Fatalf("exchange %s auth %q", r.Method, r.Header.Get("Authorization"))
	}
	var body struct {
		Repositories []string `json:"repositories"`
	}
	if e := json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &body); e != nil || len(body.Repositories) != 1 || body.Repositories[0] != "app" {
		t.Fatalf("exchange body %q", f.bodies[len(f.bodies)-1])
	}
	if _, e := a.InstallationToken(context.Background(), "42x", "app"); e == nil {
		t.Fatal("non-numeric installation accepted")
	}
}

func TestInstallationTokenExchangeFailures(t *testing.T) {
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for name, f := range map[string]*fakeAppAPI{
		"401":           {status: 401},
		"404":           {status: 404},
		"200 not 201":   {status: 200},
		"no token":      {body: `{"expires_at":"2026-11-08T13:00:00Z"}`},
		"no expiry":     {body: `{"token":"ghs_x"}`},
		"bad expiry":    {body: `{"token":"ghs_x","expires_at":"tomorrow"}`},
		"expired":       {body: `{"token":"ghs_x","expires_at":"2026-11-08T11:00:00Z"}`},
		"control chars": {body: `{"token":"ghs_x\r\nX-Evil: 1","expires_at":"2026-11-08T13:00:00Z"}`},
		"oversized":     {body: `{"token":"` + strings.Repeat("a", 2<<20) + `","expires_at":"2026-11-08T13:00:00Z"}`},
	} {
		f.now = func() time.Time { return base }
		a, _ := newAppUnderTest(t, f)
		_, e := a.InstallationToken(context.Background(), "4242", "app")
		if e == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		for _, r := range f.requests {
			if jwt, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); jwt != "" && strings.Contains(e.Error(), jwt) {
				t.Errorf("%s: error contains the JWT", name)
			}
		}
		if strings.Contains(e.Error(), "ghs_x") {
			t.Errorf("%s: error contains the token", name)
		}
		src := a.TokenSource("4242", "app")
		if _, e := src.Token(context.Background()); e == nil {
			t.Errorf("%s: token source accepted", name)
		}
	}
}

func TestAppRequestsDoNotFollowRedirects(t *testing.T) {
	other := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other++ }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	a := &App{BaseURL: srv.URL, AppID: "123456", Key: appTestKey(t)}
	if _, e := a.Identity(context.Background()); e == nil {
		t.Fatal("redirected identity accepted")
	}
	if _, e := a.InstallationToken(context.Background(), "4242", "app"); e == nil {
		t.Fatal("redirected exchange accepted")
	}
	if other != 0 {
		t.Fatal("JWT followed a redirect to another host")
	}
}

func TestInstallationTokenSourceCachesAndRefreshes(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	f := &fakeAppAPI{now: clock.now, ttl: time.Hour}
	a, _ := newAppUnderTest(t, f)
	src := a.TokenSource("4242", "app")
	ctx := context.Background()
	t1, e := src.Token(ctx)
	if e != nil || t1 != "ghs_unit001" {
		t.Fatalf("%q %v", t1, e)
	}
	if t2, _ := src.Token(ctx); t2 != t1 || f.exchanges() != 1 {
		t.Fatalf("token not cached: %q exchanges %d", t2, f.exchanges())
	}
	// Still outside the refresh margin: cached.
	clock.add(time.Hour - TokenRefreshMargin - time.Second)
	if t3, _ := src.Token(ctx); t3 != t1 || f.exchanges() != 1 {
		t.Fatalf("refreshed early: %q exchanges %d", t3, f.exchanges())
	}
	// Inside the margin: refreshed.
	clock.add(2 * time.Second)
	t4, e := src.Token(ctx)
	if e != nil || t4 != "ghs_unit002" || f.exchanges() != 2 {
		t.Fatalf("not refreshed inside the margin: %q %v exchanges %d", t4, e, f.exchanges())
	}
	if t5, _ := src.Token(ctx); t5 != t4 || f.exchanges() != 2 {
		t.Fatal("refreshed token not cached")
	}
	if TokenRefreshMargin != 5*time.Minute {
		t.Fatalf("margin %v", TokenRefreshMargin)
	}
}

func TestInstallationTokenSourceUsesShortTokenOnce(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	f := &fakeAppAPI{now: clock.now, ttl: 2 * time.Minute} // already inside the margin
	a, _ := newAppUnderTest(t, f)
	src := a.TokenSource("4242", "app")
	if tok, e := src.Token(context.Background()); e != nil || tok != "ghs_unit001" || f.exchanges() != 1 {
		t.Fatalf("short-lived token: %q %v exchanges %d", tok, e, f.exchanges())
	}
	if tok, _ := src.Token(context.Background()); tok != "ghs_unit002" || f.exchanges() != 2 {
		t.Fatalf("token inside the margin not refreshed on next use: %q", tok)
	}
}

func TestInstallationTokenSourceIsConcurrencySafe(t *testing.T) {
	f := &fakeAppAPI{}
	a, _ := newAppUnderTest(t, f)
	src := a.TokenSource("4242", "app")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := src.Token(context.Background()); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if n := f.exchanges(); n < 1 || n > 16 {
		t.Fatalf("exchanges %d", n)
	}
}

type countingSource struct {
	n   int
	err error
}

func (s *countingSource) Token(context.Context) (string, error) {
	s.n++
	return fmt.Sprintf("tok-%d", s.n), s.err
}

func TestClientAsksTokenSourcePerRequest(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/acme/app":
			fmt.Fprint(w, `{"id":10,"default_branch":"main","full_name":"acme/app"}`)
		case "/repos/acme/app/pulls/1":
			fmt.Fprint(w, `{"id":1,"head":{"sha":"aa","repo":{"id":10}},"base":{"sha":"bb","repo":{"id":10}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	src := &countingSource{}
	c := NewClientWithTokenSource(srv.URL, src, nil, 1<<20)
	if _, e := c.Repository(context.Background(), "acme", "app"); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Change(context.Background(), "acme", "app", 1); e != nil {
		t.Fatal(e)
	}
	if len(seen) != 2 || seen[0] != "Bearer tok-1" || seen[1] != "Bearer tok-2" {
		t.Fatalf("authorization headers %v", seen)
	}
	failing := &countingSource{err: fmt.Errorf("key unavailable")}
	c = NewClientWithTokenSource(srv.URL, failing, nil, 1<<20)
	if _, e := c.Repository(context.Background(), "acme", "app"); e == nil || len(seen) != 2 {
		t.Fatalf("request sent although the token source failed: %v %d", e, len(seen))
	}
	if _, e := c.Change(context.Background(), "acme", "app", 1); e == nil || len(seen) != 2 {
		t.Fatalf("Change sent although the token source failed: %v %d", e, len(seen))
	}
}

func TestBotLoginAndUserID(t *testing.T) {
	if BotLogin("review-app") != "review-app[bot]" {
		t.Fatal(BotLogin("review-app"))
	}
	f := &fakeAppAPI{}
	_, srv := newAppUnderTest(t, f)
	c := NewClient(srv.URL, "ghs_x", nil, 1<<20)
	id, e := c.UserID(context.Background(), BotLogin("review-app"))
	if e != nil || id != "9001" {
		t.Fatalf("%q %v", id, e)
	}
	if got := f.requests[len(f.requests)-1].Header.Get("Authorization"); got != "Bearer ghs_x" {
		t.Fatalf("authorization %q", got)
	}
	for _, login := range []string{"", "../app", "a/b", "x[bot][bot]", "-x", "a b"} {
		if _, e := c.UserID(context.Background(), login); e == nil {
			t.Errorf("login %q accepted", login)
		}
	}
}
