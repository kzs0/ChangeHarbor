# changeharbor.yaml reference

One file configures every repository. ChangeHarbor finds it at `--config`,
then `$CHANGEHARBOR_CONFIG`, then `./changeharbor.yaml`. Relative paths inside
it resolve against the file's own directory.

```yaml
# changeharbor.yaml
version: 1
state: /var/lib/changeharbor
models:
  default: models/review.json
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: GITLAB_TOKEN
    mirror:
      path: /srv/mirrors/app.git
    policy: policies/app.json
    publish:
      approvals: submit
      comments: post
```

!!! tip "Editor completion"
    `reviewd config init` writes `changeharbor.schema.json` and starts the file
    with `# yaml-language-server: $schema=changeharbor.schema.json`. Editors
    using the YAML language server then complete and document every key.
    `reviewd config schema` prints the schema.

## Rules that apply everywhere

- **Omit a key to get its default.** An empty value (`branch: ""` or
  `branch:`) is an error, never "use the default".
- **Unknown keys are errors**, and errors say where and suggest a fix:
  `repositories.app.gitlab.ci_trus: unknown key; did you mean "ci_trust"?`
- **Plain YAML only.** Anchors, aliases, merge keys (`<<`), custom tags,
  duplicate keys and multiple documents are rejected.
- **Unquoted numbers stay as written** in text fields: `bot_user_id: 12345`,
  `branch: 1.10`. `true`/`false` in a text field is an error. Quote it.
- **No other environment variables** change behaviour. Only `$CHANGEHARBOR_CONFIG`,
  `$CHANGEHARBOR_STATE` and the secrets you name are read.

## Top level

| Key | Default | Meaning |
| --- | --- | --- |
| `version` | required | Always `1`. |
| `$schema` | — | Optional schema path, for editors that prefer a key over the modeline comment. |
| `state` | `.changeharbor` beside this file | State directory: reviews, audit records, jobs. Overridden by `$CHANGEHARBOR_STATE`, then `--state`. |
| `service` | — | Needed only for [`reviewd serve`](../guides/service.md). |
| `models` | — | Named [model config](models.md) files: `{name: path}`. |
| `repositories` | required | Repositories by short name (lowercase letters, digits, `-`, `_`). |

### `service`

| Key | Default | Meaning |
| --- | --- | --- |
| `service.address` | required | Listen address. Must be a loopback IP, e.g. `127.0.0.1:8080`; put a TLS proxy in front. |
| `service.gitlab_secret_env` | — | Variable holding the GitLab webhook secret token. |
| `service.github_secret_env` | — | Variable holding the GitHub webhook secret. |
| `service.reconciliation_interval` | `5m` | How often the change journal is reconciled with the mirror. |
| `service.context_rebuild_interval` | `24h` | How often repository context snapshots are rebuilt. |

At least one webhook secret is required.

## Repository keys

Under `repositories.<name>`:

| Key | Default | Meaning |
| --- | --- | --- |
| `url` | required | Repository web URL. Provider, web/API bases and path are derived from it. |
| `provider` | from host | `github` or `gitlab`. Needed only for self-managed hosts. |
| `api_url` | derived | API base: `https://api.github.com`, `https://HOST/api/v3` (GitHub Enterprise) or `https://HOST/api/v4` (GitLab). Override for path-prefixed installs. |
| `token_env` | required | Variable holding the forge token. |
| `token_scheme` | `private-token` | GitLab only: `private-token` or `bearer`. |
| `bot_user_id` | from the lock | The bot's numeric user ID. Resolved from `/user`; set it for GitHub App tokens. |
| `fork_source` | — | The one fork whose MRs/PRs are accepted, e.g. `contributors/app`. |
| `branch` | default branch, pinned in the lock | Target branch reviews and approvals are for. |
| `policy` | required | [Risk policy](policy.md) file. |
| `model` | `default` if defined | Name of a model in `models`. |
| `response` | — | Saved model response for offline trials. Mutually exclusive with `model`. |

### `mirror`

| Key | Default | Meaning |
| --- | --- | --- |
| `mirror.path` | required | Local git mirror. Source is read only from here. |
| `mirror.sync` | `auto` | `auto` fetches from `origin` when the mirror has it; `none` never fetches; any other value names a remote configured in the mirror. |

### `publish`

| Key | Default | Values |
| --- | --- | --- |
| `publish.approvals` | `shadow` | `off`: nothing. `shadow`: evaluate every live requirement and report *would approve* or *would withhold*. `submit`: approve the reviewed commit. |
| `publish.comments` | `shadow` | `off`: nothing. `shadow`: render the comment only. `post`: create or update the owned summary comment. |
| `publish.protections` | `required` | `required`: approve only when the forge's own protections are in place (GitLab merge and approval settings and protected branch; GitHub strict branch protection). `advisory`: approve regardless of protection settings. `doctor` then reports gaps as warnings. Must be set explicitly. |

Whatever the protection mode, ChangeHarbor withdraws its own approval when the
change or its target moves.

### `gitlab`

| Key | Default | Meaning |
| --- | --- | --- |
| `gitlab.ci_trust` | `external_config` | How the head pipeline's definition is trusted. `external_config`: `ci_config_path` must load the pipeline from another project. `unchanged_config`: withhold MRs that touch `.gitlab-ci.yml`, a local `ci_config_path` or any `gitlab.ci_paths`. `none`: always withhold approval. |
| `gitlab.ci_paths` | — | With `unchanged_config`: globs for every file that defines job behaviour, e.g. `.gitlab/ci/**`, `scripts/ci-*.sh`. |
| `gitlab.required_jobs` | the policy's `required_checks` | Job names that must succeed, exactly once, in the trusted head pipeline. |

### `github`

| Key | Default | Meaning |
| --- | --- | --- |
| `github.required_checks` | — | Check name → ID of the GitHub App that must have produced it, e.g. `test: "15368"` (GitHub Actions). |

### `summary`

Optional background summaries of the repository, used as review context.

| Key | Default | Meaning |
| --- | --- | --- |
| `summary.model` | required | Name of a model in `models`. |
| `summary.limits` | required | Independent token and byte limits for summary generation. |

## Which changes invalidate existing reviews

A saved review can only be published while its **review identity** is
unchanged: `url`, `provider`, `api_url`, `fork_source`, `mirror.path`, `policy`
(and the policy file's contents, except `auto_approve_max`) and `branch`.
Everything under `publish`, `gitlab`, `github`, tokens and sync settings can
change freely. `reviewd config check` lists the identity keys.

## The lock file

`reviewd config resolve` writes `changeharbor.lock.yaml` next to the config
with the IDs it looked up: repository and fork IDs, default branch and bot
user. Commit it. It records the settings it was resolved from. If `url`,
`provider`, `api_url`, `fork_source`, `token_env` or `bot_user_id` change,
commands stop with *lock is stale … run reviewd config resolve*.
