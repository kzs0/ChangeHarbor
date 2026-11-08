# Unified configuration

One reviewed file, `changeharbor.yaml`, replaces per-command forge and service
files. A sibling lock file, `changeharbor.lock.yaml`, pins identities resolved from
the forge API. Policies and model configs stay separate files referenced by
path and name: they are separately digested trust artifacts, and model
configs are reused across repositories.

```yaml
# changeharbor.yaml
version: 1
state: /data/changeharbor
service:
  address: 127.0.0.1:8080
  gitlab_secret_env: GITLAB_WEBHOOK_SECRET
models:
  default: models/review.json
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: GITLAB_TOKEN
    mirror:
      path: /mirrors/app.git
    policy: policies/app.json
    publish:
      approvals: submit
      comments: post
```

The file is YAML so it can carry comments. Parsing is strict: nulls,
anchors and aliases, merge keys, custom tags, duplicate keys and multiple
documents are rejected with line numbers. Unquoted numbers in text fields keep
their exact text (`bot_user_id: 12345`, `branch: 1.10`). JSON is valid YAML and
still accepted.

## Principles

- **Derive, don't ask.** The provider, web and API bases, and owner/path come
  from `url`. Project ID, default branch, bot user and fork source IDs are
  resolved once by `reviewd config resolve` into the lock file, which is
  reviewed and committed. The lock records the inputs it was resolved from;
  any change to them makes it stale, and commands refuse to run until it is
  resolved again.
- **Explicit modes, safe defaults, no empty strings.** An omitted key means
  the documented default. An empty string is always an error. Defaults never
  write to a forge.

  | Key | Values | Default |
  | --- | --- | --- |
  | `publish.approvals` | `off`, `shadow` (evaluate everything, write nothing), `submit` | `shadow` |
  | `publish.comments` | `off`, `shadow` (render only), `post` | `shadow` |
  | `publish.protections` | `required`, `advisory` (approve regardless of forge protections) | `required` |
  | `mirror.sync` | `auto` (use `origin` if configured), `none`, remote name | `auto` |
  | `branch` | branch name | default branch from the lock |
  | `token_scheme` (GitLab) | `private-token`, `bearer` | `private-token` |
  | `gitlab.ci_trust` | `external_config`, `unchanged_config`, `none` | `external_config` |
  | `gitlab.required_jobs` | job names | the policy's `required_checks` |
  | `model` | name in `models` | `default` when defined |
  | `state` | directory | `.changeharbor` beside the config |

- **Structural review identity.** Only the review identity (provider, bases,
  repository path and IDs, mirror path, policy path, branch) is digested into
  review provenance. Publication settings never invalidate reviews, by
  construction, and a migrated legacy configuration keeps the same identity,
  so existing reviews stay valid after migration.
- **Relative paths** resolve against the config file's directory.
- A repository may set `model` or `response`, but not both. When neither is
  set (as after `init` or `migrate`), `--model-config` or `--response` on the
  command line supplies the model.
- **Errors name the path** (`repositories.app.gitlab.ci_trust`) and suggest
  close matches for unknown keys and invalid values.
- **Environment overrides only locate things:** `CHANGEHARBOR_CONFIG` (the config
  path) and `CHANGEHARBOR_STATE`. Trust and publication settings come only from
  the reviewed file. Precedence for state is flag, then environment, then
  file, then default.

## Commands

- `config init --url … --mirror … --policy … --token-env …` writes a
  commented `changeharbor.yaml` and `changeharbor.schema.json`, then resolves the lock. It never
  overwrites existing files.
- `config resolve` (re)writes the lock from the forge API.
- `config check` validates and prints the effective configuration per
  repository, the source of each defaulted value, and which keys invalidate
  existing reviews.
- `config schema` prints the JSON Schema for editor completion.
- `config migrate --forge-config F [--server-config S]` converts legacy files
  offline (their IDs become the lock).

Every command accepts `--config`. Without it the config path comes from
`CHANGEHARBOR_CONFIG`, then `./changeharbor.yaml`. `review --url` and `publish`/`comment
--run` select the repository by URL. Legacy `--forge-config` keeps working for
one release with a deprecation warning. `serve --config` accepts either
format.
