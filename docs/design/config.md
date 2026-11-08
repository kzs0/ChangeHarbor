# Unified configuration

One reviewed file, `reviewd.json`, replaces per-command forge and service
files. A sibling lock file, `reviewd.lock.json`, pins identities resolved from
the forge API. Policies and model configs stay separate files referenced by
path and name: they are separately digested trust artifacts, and model
configs are reused across repositories.

```json
{
  "version": 1,
  "$schema": "reviewd.schema.json",
  "state": "/data/reviewd",
  "service": { "address": "127.0.0.1:8080", "gitlab_secret_env": "GITLAB_WEBHOOK_SECRET" },
  "models": { "default": "models/review.json" },
  "repositories": {
    "app": {
      "url": "https://gitlab.com/group/app",
      "token_env": "GITLAB_TOKEN",
      "mirror": { "path": "/mirrors/app.git" },
      "policy": "policies/app.json",
      "publish": { "approvals": "submit", "comments": "post" }
    }
  }
}
```

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
  | `mirror.sync` | `auto` (use `origin` if configured), `none`, remote name | `auto` |
  | `branch` | branch name | default branch from the lock |
  | `token_scheme` (GitLab) | `private-token`, `bearer` | `private-token` |
  | `gitlab.ci_trust` | `external_config`, `unchanged_config`, `none` | `external_config` |
  | `gitlab.required_jobs` | job names | the policy's `required_checks` |
  | `model` | name in `models` | `default` when defined |
  | `state` | directory | `.reviewd` |

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
- **Environment overrides only locate things:** `REVIEWD_CONFIG` (the config
  path) and `REVIEWD_STATE`. Trust and publication settings come only from
  the reviewed file. Precedence for state is flag, then environment, then
  file, then default.

## Commands

- `config init --url … --mirror … --policy … --token-env …` writes
  `reviewd.json` and `reviewd.schema.json`, then resolves the lock. It never
  overwrites existing files.
- `config resolve` (re)writes the lock from the forge API.
- `config check` validates and prints the effective configuration per
  repository, the source of each defaulted value, and which keys invalidate
  existing reviews.
- `config schema` prints the JSON Schema for editor completion.
- `config migrate --forge-config F [--server-config S]` converts legacy files
  offline (their IDs become the lock).

Every command accepts `--config`. Without it the config path comes from
`REVIEWD_CONFIG`, then `./reviewd.json`. `review --url` and `publish`/`comment
--run` select the repository by URL. Legacy `--forge-config` keeps working for
one release with a deprecation warning. `serve --config` accepts either
format.
