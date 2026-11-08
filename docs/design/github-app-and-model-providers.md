# GitHub App auth, token-authenticated mirrors, model providers and releases

Rollout gaps 5, 6 and 7 (see the gap analysis): org rollouts want a GitHub App
rather than a machine-user seat, installation tokens expire hourly so they
can't live in a mirror's remote URL, model configs only speak OpenAI chat
completions with Bearer auth, and there is no image or release pipeline.

Everything below keeps the specification's constraints: credentials come only
from named environment variables, the model gets no tools, every request and
attempt is recorded, ambiguous attempts are never replayed, and every limit is
explicit. Only the Go standard library is added to (`crypto/rsa`,
`crypto/x509`, `encoding/pem`, `net/http/cgi` in tests); no new modules.

## 1. GitHub App authentication

### Config

```yaml
# changeharbor.yaml
version: 1
repositories:
  app:
    url: https://github.com/acme/app
    github:
      app:
        app_id: 123456
        private_key_env: CHANGEHARBOR_APP_KEY
        installation_id: 7890123   # optional; resolved into the lock
    mirror:
      path: /srv/mirrors/app.git
    policy: policies/app.json
```

| Key | Values | Default |
| --- | --- | --- |
| `github.app.app_id` | numeric App ID (text, like `bot_user_id`) | required with `github.app` |
| `github.app.private_key_env` | env var holding the App's PEM private key | required with `github.app` |
| `github.app.installation_id` | numeric installation ID | resolved by `config resolve` |

- `token_env` and `github.app` are mutually exclusive; exactly one is
  required. Both set: `repositories.<name>: set exactly one of token_env and
  github.app`. Neither: `repositories.<name>.token_env: required (or set
  github.app)`. Errors name `token_env` and `github.app`.
- `github.app` sits under the existing `github:` section, so it is already
  rejected for GitLab repositories (`github: only valid for GitHub
  repositories`). `token_env` stays for PATs, machine users and GitLab.
- Invalid values name their path: `…github.app.app_id: must be a numeric
  App ID`, `…github.app.private_key_env: invalid environment variable name`,
  `…github.app.installation_id: must be a numeric installation ID`, and
  `…github.app.app_id: required` / `…private_key_env: required`.
- The JSON schema no longer lists `token_env` as required for a repository
  (`required: [url, mirror, policy]`) and documents `github.app.*`,
  `mirror.auth` and `mirror.url`. `website/docs/configuration/reference.md`
  must document every new key (enforced by `TestDocsReferenceCoversEveryKey`).
- Works on GitHub Enterprise Server unchanged: every App endpoint is relative
  to the repository's API base (`api_url`, or `<host>/api/v3`).

Go types (`internal/config`):

```go
type GitHubSettings struct {
	RequiredChecks map[string]string  `json:"required_checks,omitempty"`
	App            *GitHubAppSettings `json:"app,omitempty"`
}
type GitHubAppSettings struct {
	AppID          string `json:"app_id"`
	PrivateKeyEnv  string `json:"private_key_env"`
	InstallationID string `json:"installation_id,omitempty"`
}
```

`RepositorySettings.TokenEnv` keeps its tag (`json:"token_env"`) and is empty
when an App is configured.

### Protocol (`internal/forge/github`)

1. **Key.** `ParsePrivateKey(pem []byte) (*rsa.PrivateKey, error)` accepts one
   PEM block, `RSA PRIVATE KEY` (PKCS#1) or `PRIVATE KEY` (PKCS#8 wrapping an
   RSA key). It rejects encrypted PEM, non-RSA keys, keys under 2048 bits,
   trailing data and garbage. Errors never include key material.
2. **JWT.** `SignAppJWT(key *rsa.PrivateKey, appID string, now time.Time)
   (string, error)`: RS256, header `{"alg":"RS256","typ":"JWT"}`, claims
   `iat = now-60s`, `exp = now+9m` (so `exp-iat` is GitHub's 10 minute
   maximum), `iss = appID` (JSON string). base64url without padding. A
   non-numeric app ID or nil key is an error.
3. **Installation.** When `installation_id` is omitted, `GET
   /repos/{owner}/{repo}/installation` (JWT) returns `{"id": N}`.
4. **Token.** `POST /app/installations/{id}/access_tokens` (JWT) with body
   `{"repositories":["<repo>"]}` (scopes the token to the one repository),
   expecting `201` and `{"token": "...", "expires_at": RFC3339}`. A missing or
   control-character token, a missing/unparseable/past `expires_at`, any other
   status, an oversized body or a redirect is an error, and nothing is cached.
5. **Cache and refresh.** An `InstallationTokenSource` caches the token in
   memory and fetches a new one when `expires_at - now < TokenRefreshMargin`
   (5 minutes). A freshly issued token is used even if it is already inside
   the margin (no loop). Safe for concurrent use. The clock is injectable
   (`App.Now`).
6. **All API calls use the installation token.** `Client` gains a token
   source; every request asks it for the current token (`Authorization: Bearer
   <installation token>`). A token-source error fails the request before it is
   sent. The JWT is only ever sent to `/app…` endpoints and
   `/repos/{o}/{r}/installation`.
7. **Process-wide cache.** `cmd/reviewd` keeps one token source per (API base,
   app ID, installation ID, public-key fingerprint) for the process, so a
   long-running `serve` refreshes hourly and a CLI invocation exchanges at
   most once while its token is valid. The key variable is read and parsed on
   every use, so a missing or invalid key fails (naming the variable, never
   echoing its value) even while a token is cached.
8. **Never logged or stored.** Tokens, JWTs and the PEM never appear in
   output, errors, the state directory or the mirror.

Exported API:

```go
type TokenSource interface{ Token(ctx context.Context) (string, error) }
func NewClientWithTokenSource(baseURL string, ts TokenSource, httpClient *http.Client, maxBytes int64) *Client

func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error)
func SignAppJWT(key *rsa.PrivateKey, appID string, now time.Time) (string, error)
const TokenRefreshMargin = 5 * time.Minute

type App struct {
	BaseURL  string           // API base, e.g. https://api.github.com
	AppID    string
	Key      *rsa.PrivateKey
	HTTP     *http.Client     // nil: 30 s timeout client
	Now      func() time.Time // nil: time.Now
	MaxBytes int64            // <= 0: 1 MiB
}
type AppIdentity struct{ ID, Slug string }
type InstallationToken struct {
	Token     string
	ExpiresAt time.Time
}
func (a *App) Identity(ctx context.Context) (AppIdentity, error)                  // GET /app; ID must equal AppID
func (a *App) Installation(ctx context.Context, owner, repo string) (string, error) // GET /repos/{o}/{r}/installation
func (a *App) InstallationToken(ctx context.Context, installationID string, repositories ...string) (InstallationToken, error)
func (a *App) TokenSource(installationID string, repositories ...string) *InstallationTokenSource
type InstallationTokenSource struct{ /* unexported */ }
func (s *InstallationTokenSource) Token(ctx context.Context) (string, error)

func BotLogin(slug string) string                                  // slug + "[bot]"
func (c *Client) UserID(ctx context.Context, login string) (string, error) // GET /users/{login}
```

Redirects are never followed for any of these calls (a credential must not
reach another host). `UserID` validates the login
(`^[A-Za-z0-9][A-Za-z0-9-]*(\[bot\])?$`) and path-escapes it.

### `config resolve` and the lock

For an App repository `resolve`:

1. `GET /app` (JWT): the returned `id` must equal `app_id` (else `GitHub
   App ID mismatch`); records `slug`.
2. Installation: the configured `installation_id`, else `GET
   /repos/{o}/{r}/installation`.
3. Exchanges an installation token and uses it for `GET /repos/{o}/{r}` (and
   the fork source, as today).
4. Bot user: `bot_user_id` from the file if set, else `GET
   /users/{slug}[bot]` with the installation token. `/user` is never called
   for an App (installation tokens can't use it).

Lock types:

```go
type LockInputs struct {
	… existing fields unchanged …
	GitHubAppID             string `json:"github_app_id,omitempty"`
	GitHubAppPrivateKeyEnv  string `json:"github_app_private_key_env,omitempty"`
	GitHubAppInstallationID string `json:"github_app_installation_id,omitempty"`
}
type LockedRepository struct {
	… existing fields unchanged …
	InstallationID string `json:"installation_id,omitempty"`
	AppSlug        string `json:"app_slug,omitempty"`
}
```

- The App settings are lock inputs: changing `app_id`, `private_key_env` or
  `installation_id` makes the lock stale (`lock is stale (github_app_id
  changed)`). New fields are `omitempty`, so existing locks stay fresh.
- `Lock.Check` treats an App repository whose entry lacks a numeric
  `installation_id` as an invalid entry.
- Runtime uses the lock's installation ID; reviews and publication never
  call `/repos/{o}/{r}/installation`.
- `config check` effective values gain `auth` (`github_app` or `token`),
  `github_app_id`, `github_installation_id` and `mirror_auth` (`none` or
  `token`); its per-repository `Sources` map gains `github_installation_id`
  (`file` or `lock`); `token_env` is empty for an App and `bot_user_id` is the
  resolved bot.
- `config.Settings` normalisation records `repositories.<name>.mirror.auth`
  as `default` or `file` in `Sources`, like other defaulted keys.

### Identity

The authentication method is **not** part of the review identity:
`reviewIdentity` is unchanged, `InvalidatesReviews` gains nothing, and a
review made with a PAT stays publishable after switching the repository to an
App (and back). Credentials are runtime, not input.

## 2. Mirror sync without credentials in the remote URL

```yaml
    mirror:
      path: /srv/mirrors/app.git
      auth: token                               # none | token
      url: https://github.com/acme/app.git      # optional
```

| Key | Values | Default |
| --- | --- | --- |
| `mirror.auth` | `none` (credentials come from the mirror's own git config, as today), `token` | `token` when `github.app` is set and `mirror.sync` is `auto`; else `none` |
| `mirror.url` | clone/fetch URL, https (loopback http allowed, like `api_url`), no credentials, query or fragment | `<url>.git` |

- `mirror.auth: token` fetches by URL (`mirror.url`) instead of a remote name,
  passing the repository's current forge token (the App installation token,
  or the `token_env` token for PATs and GitLab) to git **only through the
  environment**:

  ```
  GIT_CONFIG_COUNT=<n>
  GIT_CONFIG_KEY_i=http.<mirror.url>.extraHeader
  GIT_CONFIG_VALUE_i=Authorization: Basic base64(<user>:<token>)
  ```

  `<user>` is `x-access-token` on GitHub and `oauth2` on GitLab. The header is
  scoped to exactly that URL. The same environment also sets
  `http.followRedirects=false` and an empty `credential.helper`. Inherited
  `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_*`, `GIT_CONFIG_VALUE_*` and
  `GIT_CONFIG_PARAMETERS` are dropped. The token never appears in argv, in the
  mirror's config or anywhere on disk.
- `mirror.auth: token` requires `mirror.sync: auto`:
  `…mirror.auth: token requires mirror.sync auto`.
- **Creating the mirror.** When `mirror.path` doesn't exist and
  `mirror.sync` is `auto` (a named remote can't exist in a mirror that
  doesn't exist, and `none` never touches the network), reviewd runs
  `git clone --mirror` from
  `mirror.url` (with the header when `mirror.auth` is `token`, with no
  credential otherwise) and then the usual targeted fetch. The clone's
  `origin` URL is `mirror.url`, which has no credentials. Hooks stay disabled,
  tags aren't fetched beyond what the clone itself takes, and prompts are off
  (`GIT_TERMINAL_PROMPT=0`).
- `mirror.url` and `mirror.auth` are transport, not identity (the mirror path
  remains in the review identity; commits are verified by ID).

`internal/git` API:

```go
// HTTPAuth carries a forge token to git through the environment only.
type HTTPAuth struct {
	URL      string // remote URL the header is scoped to; https or loopback http
	Username string // "x-access-token" (GitHub) or "oauth2" (GitLab)
	Token    string // empty: no header (unauthenticated)
}
// Env returns the GIT_CONFIG_* entries described above.
func (a HTTPAuth) Env() ([]string, error)
// FetchURL is Fetch from a URL instead of a configured remote name.
func (r Reader) FetchURL(ctx context.Context, auth HTTPAuth, refspecs ...string) error
// CloneMirror creates dir with `git clone --mirror auth.URL dir`.
func CloneMirror(ctx context.Context, auth HTTPAuth, dir string) error
```

`Env`, `FetchURL` and `CloneMirror` reject URLs with userinfo, query or
fragment, non-https non-loopback URLs, option-looking URLs, and tokens or
usernames containing control characters or `:` (username). `FetchURL`
validates refspecs exactly like `Fetch` and runs `git fetch --no-tags
--no-write-fetch-head --update-head-ok --no-recurse-submodules
--end-of-options <url> <refspecs>`. All three go through the same bounded
runner as other git commands (hooks, fsmonitor, global/system config
disabled), so a hook from `GIT_TEMPLATE_DIR` never runs during a clone.
`CloneMirror` refuses a non-empty target directory and creates missing
parents. Tests observe the exact git
invocation through an unexported seam in package git:

```go
// commandHook, when set (tests only), sees every git command before it runs.
var commandHook func(*exec.Cmd)
```

## 3. Model providers

### Model config

```json
{
  "api": "anthropic_messages",
  "endpoint": "https://api.anthropic.com/v1/messages",
  "model": "review-model",
  "api_key_env": "ANTHROPIC_API_KEY",
  "capabilities": {"context_tokens": 200000, "max_output_tokens": 16000, "input_tokens_per_byte": 1, "structured_output": true},
  "limits": {"max_request_bytes": 1048576, "max_response_bytes": 1048576, "max_output_tokens": 8000, "max_attempts": 2, "timeout": "3m", "max_total_tokens": 600000}
}
```

| Key | Values | Default |
| --- | --- | --- |
| `api` | `openai_chat`, `anthropic_messages` | `openai_chat` (current behaviour) |
| `endpoint` | full URL | required for `openai_chat`; `https://api.anthropic.com/v1/messages` for `anthropic_messages` |
| `api_key_header` | HTTP header name for the key, e.g. `api-key` (Azure OpenAI) | `Authorization` (`openai_chat`), `x-api-key` (`anthropic_messages`) |
| `api_key_prefix` | word sent before the key, separated by one space | `Bearer` when the header is `Authorization`, none otherwise; `""` sends the bare key |
| `limits.timeout` | integer nanoseconds **or** a Go duration string (`"3m"`, `"90s"`) | required |

- Unknown `api` → `NewHTTP` error `unsupported model api`.
- `api_key_header` must be an RFC 7230 token and not one of `Host`,
  `Content-Type`, `Content-Length`, `Transfer-Encoding`, `Connection`,
  `anthropic-version`; `api_key_prefix` must be a single token with no spaces
  or control characters. `api_key_header`/`api_key_prefix` without
  `api_key_env` is an error.
- `limits.timeout`: `HTTPLimits` gets a custom `UnmarshalJSON` that accepts an
  integer or a duration string, still rejects unknown and duplicate keys, and
  rejects non-integer numbers and unparseable strings. Marshalling is
  unchanged (integer nanoseconds), so every digest built from `HTTPLimits`
  (claim identity, background summary digest) is byte-identical for an
  existing integer config and for the equivalent string config.

Go identifiers (`internal/model`):

```go
const (
	APIOpenAIChat            = "openai_chat"
	APIAnthropicMessages     = "anthropic_messages"
	DefaultAnthropicEndpoint = "https://api.anthropic.com/v1/messages"
	AnthropicVersion         = "2023-06-01"
)
type HTTPConfig struct {
	… existing fields …
	API          string  `json:"api,omitempty"`
	APIKeyHeader string  `json:"api_key_header,omitempty"`
	APIKeyPrefix *string `json:"api_key_prefix,omitempty"`
}
type HTTP struct {
	… existing fields …
	API          string // "" means openai_chat
	APIKeyHeader string // "" means the API's default header and prefix
	APIKeyPrefix string // used literally when APIKeyHeader is set
}
func (l *HTTPLimits) UnmarshalJSON([]byte) error
// AnthropicSchema returns a copy of schema without keywords Anthropic
// structured outputs reject (minimum, maximum, exclusiveMinimum,
// exclusiveMaximum, multipleOf, minLength, maxLength, minItems, maxItems),
// wherever they appear as keywords (a property named "minimum" is kept).
// Host validation (ValidateReview) still enforces them. Rejects invalid or
// duplicate-key JSON. Idempotent.
func AnthropicSchema(schema json.RawMessage) (json.RawMessage, error)
```

`NewHTTP` resolves defaults into `HTTP` (API, endpoint, header and prefix).
For a hand-built `HTTP` with `APIKeyHeader == ""`, the API's default header
and prefix apply.

### Anthropic Messages adapter

Shapes confirmed against the `claude-api` skill (2026-11): structured output
is `output_config.format = {"type":"json_schema","schema":…}` (the older
`output_format` is deprecated); objects need `additionalProperties: false`;
numeric and string-length constraints are not supported; usage carries
`input_tokens`, `output_tokens`, `cache_read_input_tokens`,
`cache_creation_input_tokens`; `stop_reason` is one of `end_turn`,
`max_tokens`, `stop_sequence`, `tool_use`, `pause_turn`, `refusal`; errors
are `{"type":"error","error":{"type":…,"message":…}}` with 429
(`rate_limit_error`) and 529 (`overloaded_error`) retryable. The skill lists
`anthropic-version: 2026-10-21` as current; `2023-06-01` remains supported and
our request uses none of the fields `2026-10-21` changes (no
`temperature`/`top_p`/`top_k`, documents or citations), so we send
`2023-06-01` (open question: move to `2026-10-21`).

Request (`POST endpoint`):

- Headers: `Content-Type: application/json`, `anthropic-version:
  2023-06-01`, the key header (`x-api-key: <key>` by default). No
  `Authorization` unless configured.
- Body has exactly these keys:

  ```json
  {"model": "...", "max_tokens": <limits.max_output_tokens>,
   "system": "<the same fixed system prompt as openai_chat>",
   "messages": [{"role": "user", "content": "<prompt>"}],
   "output_config": {"format": {"type": "json_schema", "schema": <AnthropicSchema(request schema)>}}}
  ```

  Never `tools`, `tool_choice`, `thinking`, `temperature` or `stream`.

Response mapping:

- `200` with `type: "message"`. Content blocks: `text` blocks are
  concatenated into `Response.Raw`; `thinking` and `redacted_thinking` are
  ignored; `tool_use`, `server_tool_use` or any other type sets `ToolCalls`.
- `stop_reason`: `end_turn` → complete; `max_tokens` → `Truncated`;
  `refusal` → `Refused`; `tool_use` → `ToolCalls`; anything else or missing →
  `Truncated`. `ValidateReview` already turns each into invalid output.
- Usage: `InputTokens = input_tokens + cache_read_input_tokens +
  cache_creation_input_tokens`, `OutputTokens = output_tokens`,
  `CachedTokens = cache_read_input_tokens` with `CachedKnown` when that field
  is present. Missing `input_tokens` or `output_tokens` → `Known=false`,
  `CostMicros=null`. The reserved-budget and pricing checks are the same as
  for chat completions.
- Status: 429, 503 and 529 are explicit rejections and are retried within
  `max_attempts` (identical bytes); any other non-200 is `model HTTP status
  N`, recorded, not retried. Errors never contain the key.
- Request byte cap, token reservation, durable claim, intent/result/usage
  records and "no ambiguous replay" are shared with the chat adapter
  unchanged.

### Identity

The claim identity adds the API only when it is not `openai_chat`: the adapter
version string is `reviewd-http-v2` for `openai_chat` (unchanged, so existing
identities and reuse keep working) and `reviewd-anthropic-v1` for
`anthropic_messages`. Switching `api` therefore never reuses a response from
the other protocol. `api_key_header`/`api_key_prefix` are auth, not identity.

### CONTRACT.md changes (internal/model)

Replace "OpenAI-compatible chat-completions adapter … limits.timeout is an
integer nanosecond duration" with: the adapter speaks `openai_chat` (default)
or `anthropic_messages`; `limits.timeout` is integer nanoseconds or a Go
duration string; the key header/prefix are configurable; 529 joins 429/503 as
a retryable explicit rejection for Anthropic; Anthropic structured output uses
`output_config.format` with unsupported schema keywords removed and enforced
by host validation; usage maps as above.

## 4. Container image and releases

- **`Dockerfile`** (repo root), multi-stage:
  - builder `golang:<go.mod major.minor>…-bookworm` (pinned tag), `CGO_ENABLED=1`,
    `go build -trimpath -o /out/reviewd ./cmd/reviewd`. Multi-arch builds use
    `FROM --platform=$BUILDPLATFORM` and a Debian cross gcc
    (`gcc-aarch64-linux-gnu`/`gcc-x86-64-linux-gnu`) chosen by `TARGETARCH`,
    so arm64 doesn't compile under emulation.
  - runtime `debian:bookworm-slim` (pinned tag; glibc matches the builder),
    installs `git` and `ca-certificates`, creates a non-root `reviewd` user
    (fixed UID 10001), `USER reviewd`, `VOLUME /data`, `WORKDIR /data`,
    `ENTRYPOINT ["/usr/local/bin/reviewd"]`.
  - No `ENV`/`ARG` carrying tokens, keys or secrets.
- **`.dockerignore`** excludes `.git`, `.cache`, `bin`, `dist`,
  `.changeharbor` and `state`.
- **`deploy/docker-compose.yml`**: the `reviewd` service (image
  `ghcr.io/kzs0/reviewd:<tag>`, `command: ["serve", "--config",
  "/config/changeharbor.yaml"]`, config mounted read-only, state on a named
  volume at `/data`, mirrors volume), secrets only as `${VAR}` substitutions or
  an `env_file`, and a `caddy` service terminating TLS in front of the
  loopback-bound listener (the service only listens on loopback, so the two
  share a network namespace via `network_mode: service:reviewd`), with a
  `deploy/Caddyfile`.
- **`.github/workflows/release.yml`**, on `push: tags: ['v*']` only:
  - top-level `permissions: contents: read`;
  - `validate` job: same as CI, `just check build`;
  - `package` job (`needs: validate`), matrix on native runners
    `ubuntu-24.04` (amd64) and `ubuntu-24.04-arm` (arm64), `VERSION=${tag}
    scripts/package.sh`, uploads the tarballs and `.sha256` files;
  - `release` job (`needs: package`, `permissions: contents: write`) attaches
    them to the GitHub release;
  - `image` job (`needs: validate`, `permissions: contents: read, packages:
    write, id-token: write, attestations: write`) builds
    `linux/amd64,linux/arm64` with buildx and pushes to `ghcr.io` with
    `provenance: true` (and an SBOM).
  - Every `uses:` names a ref. Native runners mean `scripts/package.sh` needs
    no cross-compiler; it stays host-only.

Static checks live in `cmd/reviewd/packaging_test.go` (parsing the Dockerfile
line by line and the YAML with `go.yaml.in/yaml/v3`).

## 5. Tests

End-to-end (`cmd/reviewd`):

- `github_app_e2e_test.go`: fake GitHub verifying RS256 JWTs against the
  App's public key; resolve records installation/slug/bot; configured
  installation skips lookup; App ID mismatch fails; review, publish and
  revalidate use only installation tokens and never `/user`; expiring tokens
  are refreshed; missing key env fails naming the variable; switching auth
  keeps reviews valid; token/JWT/PEM never reach output, state or mirror;
  mirror clone and fetch over loopback smart HTTP (`git http-backend` via
  `net/http/cgi`) with the Basic header (GitHub `x-access-token`, GitLab
  `oauth2`), and no credential sent when `mirror.auth` is `none`.
- `anthropic_e2e_test.go`: a forge review through a fake Messages API checks
  headers, exact body keys, schema, no tools, recorded usage and attempts,
  no key in state, reuse of a completed response, and refusal → invalid
  output.
- `packaging_test.go`: Dockerfile, `.dockerignore`, compose and release
  workflow checks.

Unit tests: `internal/forge/github/app_test.go`, `internal/model/anthropic_test.go`,
`internal/model/config_test.go`, `internal/config/app_test.go`,
`internal/git/fetchurl_test.go`.

Existing tests are unchanged.
