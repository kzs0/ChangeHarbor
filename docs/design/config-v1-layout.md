# Config layout: defaults, auth, triggers, inline policy and models

Status: decided; tests in `internal/config/layout_test.go`,
`internal/config/layout_docs_test.go`, `cmd/reviewd/layout_e2e_test.go` and
`cmd/reviewd/layout_digest_test.go`. Builds on [config.md](config.md).

`changeharbor.yaml` stays `version: 1`. Four additions make large files
shorter and the keys easier to find:

1. a top-level `defaults:` block inherited by every repository;
2. an `auth:` block per repository for every credential and bot identity key;
3. a `triggers:` block for automatic-review controls, so everything left
   under `review:` is review identity;
4. `policy:` and `models.<name>:` accept an inline mapping as well as a
   path, and policy and model files may be YAML.

Every old spelling keeps working as a deprecated alias. Nothing in this
change invalidates an existing review or makes an existing lock stale.

```yaml
# changeharbor.yaml
version: 1
models:
  default:                      # inline model config (or a path)
    api: anthropic_messages
    model: your-model
    api_key_env: ANTHROPIC_API_KEY
    capabilities: {context_tokens: 200000, max_output_tokens: 8000, input_tokens_per_byte: 1, structured_output: true}
    limits: {max_request_bytes: 400000, max_response_bytes: 200000, max_output_tokens: 4000, max_attempts: 2, timeout: 3m, max_total_tokens: 1000000}
defaults:
  auth:
    github_app: {app_id: 123456, private_key_env: CHANGEHARBOR_APP_KEY}
  policy: policies/default.yaml
  publish: {approvals: submit, comments: post}
  triggers:
    ignore_authors: ["dependabot[bot]"]
repositories:
  api:
    url: https://github.com/acme/api
    mirror: {path: /srv/mirrors/api.git}
  web:
    url: https://github.com/acme/web
    mirror: {path: /srv/mirrors/web.git}
    auth:
      github_app: {installation_id: 4242}   # merged into the inherited App
    triggers:
      drafts: review
```

## 1. `defaults`

`defaults` has the shape of a repository entry minus the keys that identify
one repository. These keys are **excluded**; using one in `defaults` is an
error, reported before anything else:

```
url  provider  api_url  fork_source  branch  mirror.path  mirror.url
auth.bot_user_id  auth.bot_username  auth.github_app.installation_id
```

Error: `defaults.url: not allowed in defaults (it identifies one
repository); set it under each repository`. `config.DefaultsExcludedKeys()`
returns exactly this list (dotted, relative to `defaults`).

Deprecated spellings (`token_env`, `token_scheme`, `bot_user_id`,
`bot_username`, `github.app`, `review.auto`) are **not** accepted in
`defaults` (it is new, so nothing depends on them):
`defaults.token_env: deprecated spelling is not accepted in defaults; use
auth.token_env`.

Unknown keys and invalid values in `defaults` are reported once at their
`defaults.` path with the usual suggestion, e.g. `defaults.publsh: unknown
key; did you mean "publish"?` and `defaults.publish.approvals: "yes" is not
one of off, shadow, submit`. Values are converted with the repository types,
so the strict YAML rules (no nulls, anchors, merge keys, tags, duplicates;
unquoted numbers in text fields kept as written) apply unchanged.

### Merge

Per repository, after the repository's own aliases are resolved (section 2):

- **Mappings deep-merge**: a key the repository sets wins; other keys are
  inherited. (`publish`, `mirror`, `review`, `triggers`, `gitlab`, `github`,
  `github.required_checks`, `auth`, `auth.github_app`, `summary`.)
- **Scalars and lists are replaced**, never concatenated. A repository
  clears an inherited list with `[]`.
- **Atomic values**: `policy` (path or inline mapping) and `summary.limits`
  are replaced as a whole; an inline policy is never merged field by field.
- **Exclusive pairs**: if the repository sets `auth.token_env`, the
  inherited `auth.github_app` is dropped, and if it sets any
  `auth.github_app` key, the inherited `auth.token_env` is dropped.
  Likewise a repository `model` drops an inherited `response` and vice
  versa. Inside `auth.github_app` the normal deep merge applies, so a
  repository can add its `installation_id` to an inherited App.
- **Provider-specific keys** in `defaults` apply only to matching
  repositories: `gitlab.*` and `auth.token_scheme` only to GitLab
  repositories, `github.*` only to GitHub repositories. They never cause the
  "only valid for GitLab/GitHub repositories" error on the other provider.
- Relative paths in `defaults` (`policy`, `response`) resolve against the
  config file's directory, exactly like repository paths.
- `Settings.Defaults` (`json.RawMessage`, `json:"defaults,omitempty"`) keeps
  the block as written (after strict conversion, before merging); merged
  values are in each `RepositorySettings`. Being raw, it adds only
  `defaults` to the documented key paths; the reference documents its keys
  by reference to the repository keys.

Validation (required keys, enums, credential exclusivity) runs on the merged
result and names the repository path (`repositories.app.policy: required`).

### Sources

`config.Sources` gains the value `defaults` for every leaf inherited from
`defaults` (alongside `file` and `default`; the CLI adds `lock`, `policy`,
`env`, `flag`). It now records every leaf of a repository's effective
configuration: `file` when the repository entry sets it, `defaults` when
inherited, `default` when the documented default applies. A leaf is a
scalar, a list, a map entry (`github.required_checks.lint`), or an atomic
value (`policy`, `summary.limits`). `config check` shows it under each repository's `Sources`,
e.g. `"publish.approvals": "defaults"`, `"auth.token_env": "defaults"`,
`"policy": "defaults"`.

### Identity and locks

Review identity and lock inputs are computed from **effective** values, so
moving a value from a repository into `defaults` (or back) changes neither:
existing reviews stay publishable and the lock stays fresh. A relative
policy path in `defaults` resolves to the same absolute path as before.

## 2. `auth`

```yaml
auth:
  token_env: GITLAB_TOKEN          # or github_app, exactly one
  token_scheme: bearer             # GitLab only; default private-token
  github_app:
    app_id: 123456
    private_key_env: CHANGEHARBOR_APP_KEY
    installation_id: 4242          # optional; resolved into the lock
  bot_user_id: 5                   # optional; resolved into the lock
  bot_username: reviewd-bot        # optional
```

Allowed in `defaults` except `bot_user_id`, `bot_username` and
`github_app.installation_id`.

| Deprecated | New |
| --- | --- |
| `token_env` | `auth.token_env` |
| `token_scheme` | `auth.token_scheme` |
| `bot_user_id` | `auth.bot_user_id` |
| `bot_username` | `auth.bot_username` |
| `github.app` | `auth.github_app` |
| `review.auto` | `triggers` (section 3) |

- Using a deprecated key adds one warning per occurrence to
  `Settings.Warnings`, worded `repositories.app.token_env is deprecated; use
  repositories.app.auth.token_env`. Every CLI command that loads the file
  prints each warning once to stderr as `warning: <text>`; `config check`
  also lists them in its JSON report as top-level `Warnings`.
- Setting both spellings in one repository is an error:
  `repositories.app: token_env and auth.token_env are both set; remove the
  deprecated token_env`.
- Validation errors name the key as the file spells it:
  `repositories.app.github.app.app_id: must be a numeric App ID` for the old
  spelling, `repositories.app.auth.github_app.app_id: …` for the new one.
  The exclusivity error is `set exactly one of auth.token_env and
  auth.github_app` (or `… token_env and github.app` for the old spelling).
  `auth.github_app` on a GitLab repository: `repositories.app.auth.github_app:
  only valid for GitHub repositories`.
- `LockInputs` is unchanged and is computed from effective values, so moving
  from the old to the new spelling (or into `defaults`) leaves an existing
  lock fresh.

After `DecodeSettings`, `RepositorySettings.Auth` is non-nil and holds the
effective values. The legacy Go fields (`TokenEnv`, `TokenScheme`,
`BotUserID`, `BotUsername`, `GitHub.App`) are kept populated with the same
effective values for compatibility (`GitHub` is non-nil on a GitHub App
repository and stays nil on GitLab). `config.Sources` records
`auth.token_env`, `auth.token_scheme`, `auth.github_app.app_id`, … and keeps
the old `token_scheme` key mirrored with the same value.

### `mirror.auth` default made visible

The default is unchanged: `token` for a GitHub App repository with
`mirror.sync: auto`, else `none`. `config.Sources` still says `default`;
`config check` reports the reason: `"mirror.auth": "default (github_app)"`
when the App rule chose `token`, plain `"default"` otherwise, and `file` or
`defaults` when set.

`config check` Sources also gains `auth.bot_user_id`, `auth.bot_username`
and `auth.github_app.installation_id` (same values as the existing
`bot_user_id`, `bot_username`, `github_installation_id` keys, which stay).

## 3. `triggers`

`triggers` (per repository and in `defaults`) has the fields of the old
`review.auto`: `drafts` (`skip` default, `review`), `ignore_authors`,
`skip_labels`, `skip_title_keywords`. Same validation, paths
`repositories.app.triggers.drafts: "never" is not one of skip, review`.
`review.auto` is a deprecated alias (warning; both set is an error).

After decode `RepositorySettings.Triggers` is non-nil with effective values
and `Review.Auto` mirrors it. Sources record `triggers.drafts` and mirror the
old `review.auto.drafts` key with the same value.

Now every key under `review` is review identity. `config.IdentityKeys()`
returns the repository-relative keys whose change invalidates reviews:

```
url provider api_url fork_source mirror.path policy branch
review.instructions review.path_instructions review.exclude review.large_changes
```

It must contain `review.<k>` for every `ReviewSettings` key except the
deprecated `auto`, and nothing under `triggers`, `auth`, `publish`,
`gitlab`, `github`, `mirror.sync`, `mirror.auth` or `mirror.url`.
`config check` reports exactly this list as `InvalidatesReviews` (it moves
from `cmd/reviewd` into the config package).

## 4. Inline policy and model configs; YAML files

### Policy

`policy:` is a path (string) or an inline mapping with the flat policy
fields (`base_minimum`, `auto_approve_max`, `required_checks`,
`path_rules`, …; the `version`/`merge_risk` form is accepted in files only).

- Inline policies are checked with the settings rules at their config path:
  `repositories.app.policy.path_rules[0].minimun: unknown key; did you mean
  "minimum"?`, `repositories.app.policy.base_minimum: must be a number`, and
  then `policy.Validate`: `repositories.app.policy: invalid merge risk
  policy`. Anything but a string or mapping: `repositories.app.policy: must
  be a path or a mapping`. In `defaults` the path starts `defaults.policy`.
- A path ending `.yaml`/`.yml` is read as YAML with the same strict rules
  (errors carry line numbers), then decoded by the same strict policy
  decoder; errors start with the file path and name keys
  (`policy.yaml: path_rules[0].minimun: unknown key; …`). Any other
  extension is JSON, read exactly as today.
- Policy and model **files** are read when used (`config check`, review,
  publish), not by `DecodeSettings`; inline ones are validated by
  `DecodeSettings`.
- Go: `RepositorySettings.Policy` stays the absolute path ("" when inline);
  `RepositorySettings.InlinePolicy *policy.Policy` (`json:"-"`) holds an
  inline policy. `RepositorySettings.LoadPolicy()` returns either.
  `config.LoadPolicy(path)` loads a JSON or YAML policy file.

**Digest.** The policy digest is, for every form, `hex(sha256(json.Marshal(p)))`
where `p` is the decoded `policy.Policy` with `AutoApproveMax` set to 0 —
exactly today's `fileDigest`. A JSON file therefore keeps its digest
byte-for-byte, and a YAML file or inline mapping with the same content has
the same digest. `auto_approve_max`-only changes never change it.
`cmd/reviewd` exposes `policyDigest(policy.Policy) string`; `fileDigest(path)`
keeps its signature and also reads YAML.

**Identity.** `reviewIdentity.PolicyFile` stays the absolute path for file
policies (so existing JSON setups keep their identity) and is the fixed
marker `inline` for inline policies, so moving an inline policy between a
repository and `defaults` keeps the identity. Switching between a file and
an inline policy changes the reference and therefore invalidates reviews
(the content digest itself stays equal). `config check` effective `policy`
is the path or `inline`; effective `policy_digest` is the digest.

### Models

`models.<name>:` is a path or an inline mapping with the `model.HTTPConfig`
fields. Inline mappings and `.yaml`/`.yml` files use the strict YAML rules
and the settings key checks (`models.default.limits.timout: unknown key; did
you mean "timeout"?`), then `model.DecodeHTTPConfig`. `limits.timeout`
accepts a duration string (`3m`, `90s`) or integer nanoseconds.
`api_key_prefix: ""` is allowed (it means no prefix). JSON model files are
read exactly as today.

Go: `Settings.Models[name]` stays the absolute path ("" for an inline
model); `Settings.InlineModels map[string]model.HTTPConfig` (`json:"-"`)
holds inline ones; `Settings.ModelConfig(name)` returns either (loading the
file); `config.LoadModelConfig(path)` loads a JSON or YAML file. The model
is not review identity. `config check` effective `model` is the path or
`inline`.

## Schema

`changeharbor.schema.json` gains `defaults` (repository properties minus the
excluded keys, deprecated keys omitted), `auth` (with `github_app`),
`triggers`; `policy` and `models.*` become `oneOf` string / object (the
object listing the policy or model fields). The deprecated keys stay, with
`"deprecated": true` and a description naming the new key. Every key keeps
a description (`TestSchemaCoversEverySettingsKey`).

## Commands

- `config init` writes the new layout: `auth:` with `token_env:` nested.
- `config migrate --forge-config …` (legacy files) writes the new layout.
- `config check` adds top-level `Warnings`, per-repository `ReviewIdentity`
  (the review identity digest stored as `ConfigDigest` in review
  provenance), and effective `policy_digest`.
- **`config migrate --layout [--config PATH] [--write]`** rewrites a
  `changeharbor.yaml` from the deprecated spellings to the new layout. It is
  offline (no lock or forge needed). It edits the yaml.v3 node tree, so
  comments travel with the keys they belong to; indentation is normalised to
  two spaces and flow style becomes block style. Without `--write` it prints
  the new file to stdout and one `migrated: <old> -> <new>` line per change
  to stderr. With `--write` it replaces the file atomically and prints JSON
  `{"Config": path, "Changes": [...]}`. It does not hoist values into
  `defaults`. Both spellings in one repository: exit 2 with the conflict
  error. The result must decode without warnings and with identical
  `LockInputs` and review identity, so the lock stays fresh; running it
  again changes nothing (`Changes` empty, same bytes). Library entry point:
  `config.MigrateLayout(raw []byte) (out []byte, changes []string, err error)`,
  changes worded `repositories.app.token_env -> repositories.app.auth.token_env`.
  It refuses input that does not decode. Moving `github.app` removes an
  emptied `github` block, and moving `review.auto` removes an emptied
  `review` block.

## Docs

`website/docs/configuration/reference.md` gets sections `` ### `defaults` ``
(every excluded key in backticks, and the merge rules: mappings merge,
scalars and lists are replaced, `[]` clears), `` ### `auth` `` (every
`auth.*` key as a full dotted path), `` ### `triggers` `` (every
`triggers.*` key), one table row per deprecated key that contains the old
key, the new key and the word "deprecated", a mention of
`reviewd config migrate --layout`, and, under "## Which changes invalidate
existing reviews", every `IdentityKeys()` key plus how `triggers` and
`defaults` affect reviews. `policy.md` and `models.md` each show a
validated inline example and say files may be `.yaml`; `models.md` shows
`timeout: 3m`. Documentation examples (`# changeharbor.yaml` blocks) use the
new layout only: each must decode without warnings, and together they show
`defaults`, `auth.token_env`, `auth.github_app`, `triggers`, an inline
policy and an inline model. Backticked `defaults.<key>` in prose must be a
repository key allowed in defaults (an excluded key only on a page that
says "not allowed in defaults"). The existing rollout docs tests keep
passing through the compatibility mirrors.

## New identifiers

`internal/config`:

| Identifier | Meaning |
| --- | --- |
| `Settings.Defaults json.RawMessage` (`defaults`) | the defaults block as written |
| `Settings.InlineModels map[string]model.HTTPConfig` (`json:"-"`) | inline model configs by name |
| `Settings.Warnings []string` (`json:"-"`) | deprecation warnings, one per deprecated key used |
| `func (Settings) ModelConfig(name string) (model.HTTPConfig, error)` | inline config, or the file loaded with `LoadModelConfig`; error for an undefined name |
| `type AuthSettings struct { TokenEnv, TokenScheme string; GitHubApp *GitHubAppSettings; BotUserID, BotUsername string }` | json `token_env`, `token_scheme`, `github_app`, `bot_user_id`, `bot_username`, all `omitempty` |
| `RepositorySettings.Auth *AuthSettings` (`auth,omitempty`) | non-nil after decode, effective values |
| `RepositorySettings.Triggers *AutoReviewSettings` (`triggers,omitempty`) | non-nil after decode, effective values |
| `RepositorySettings.InlinePolicy *policy.Policy` (`json:"-"`) | inline policy, nil for a file policy |
| `func (RepositorySettings) LoadPolicy() (policy.Policy, error)` | inline policy or `LoadPolicy(r.Policy)` |
| `func LoadPolicy(path string) (policy.Policy, error)` | JSON (as today) or `.yaml`/`.yml` policy file; errors name the file |
| `func LoadModelConfig(path string) (model.HTTPConfig, error)` | JSON (as today) or `.yaml`/`.yml` model file; errors name the file |
| `func DefaultsExcludedKeys() []string` | keys not allowed in `defaults` |
| `func IdentityKeys() []string` | repository-relative review identity keys |
| `func MigrateLayout(raw []byte) (out []byte, changes []string, err error)` | deprecated spellings to the new layout |

Unchanged and still populated (compatibility mirrors of the effective
values): `RepositorySettings.TokenEnv`, `TokenScheme`, `BotUserID`,
`BotUsername`, `GitHub.App`, `App()`, `Review.Auto`; `Settings.Models`
(path, "" for inline); `LockInputs` and `Lock.Check`.

`cmd/reviewd`:

| Identifier | Meaning |
| --- | --- |
| `func policyDigest(p policy.Policy) string` | `identityID(p)` with `AutoApproveMax` zeroed |
| `fileDigest(path string) (string, error)` | unchanged signature; reads YAML too; `policyDigest` of the loaded policy |
| `config check` JSON: `Warnings []string`, `Repositories.<n>.ReviewIdentity string`, effective `policy_digest`, effective `policy`/`model` = `inline` for inline forms | |
| `reviewd config migrate --layout [--config P] [--write]` | see Commands |

## Tests

- `internal/config/layout_test.go`: merge semantics and sources, excluded
  keys, defaults error paths, credential/model pairs, atomic policy,
  identity and lock inputs unchanged by moving into defaults or to the new
  spelling, alias warnings and conflicts, auth validation paths, triggers,
  `IdentityKeys`, inline policy, YAML policy and model files, inline models,
  schema, `MigrateLayout`.
- `internal/config/layout_docs_test.go`: reference sections for
  `defaults`, `auth`, `triggers`, deprecated keys, identity keys; examples use
  the new layout and show every new block; inline/YAML on the policy and
  model pages; backticked `auth.`/`triggers.`/`defaults.` keys exist.
- `cmd/reviewd/layout_digest_test.go`: golden policy digests across JSON,
  YAML and inline forms and `auto_approve_max` changes.
- `cmd/reviewd/layout_e2e_test.go`: the CLI end to end (see each test's
  name).
