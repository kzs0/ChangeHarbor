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
| `token_env` | required unless `github.app` | Variable holding the forge token. Set exactly one of `token_env` and `github.app`. |
| `token_scheme` | `private-token` | GitLab only: `private-token` or `bearer`. |
| `bot_user_id` | from the lock | The bot's numeric user ID. Resolved from `/user`, or for a GitHub App from its `<slug>[bot]` user. |
| `fork_source` | — | The one fork whose MRs/PRs are accepted, e.g. `contributors/app`. |
| `branch` | default branch, pinned in the lock | Target branch reviews and approvals are for. |
| `policy` | required | [Risk policy](policy.md) file. |
| `model` | `default` if defined | Name of a model in `models`. |
| `response` | — | Saved model response for offline trials. Mutually exclusive with `model`. |

### `mirror`

| Key | Default | Meaning |
| --- | --- | --- |
| `mirror.path` | required | Local git mirror. Source is read only from here. |
| `mirror.sync` | `auto` | `auto` fetches from `origin` when the mirror has it, and clones the mirror from `mirror.url` when `mirror.path` doesn't exist; `none` never fetches; any other value names a remote configured in the mirror. |
| `mirror.auth` | `token` for a GitHub App with `sync: auto`, else `none` | `none`: git uses the mirror's own configuration for credentials. `token`: clone and fetch `mirror.url` with the repository's forge token (the App installation token or `token_env`), passed to git only through its environment, never in the URL, argv or on disk. Requires `sync: auto`. |
| `mirror.url` | `url` + `.git` | Clone and fetch URL. https (loopback http allowed); no credentials, query or fragment. |

### `publish`

| Key | Default | Values |
| --- | --- | --- |
| `publish.approvals` | `shadow` | `off`: nothing. `shadow`: evaluate every live requirement and report *would approve* or *would withhold*. `submit`: approve the reviewed commit. |
| `publish.inline_comments` | `shadow` | `off`: nothing. `shadow`: print the inline comments it would post (no remote calls). `post`: one comment per finding on its changed line; a finding no longer reported has its comment marked *possibly outdated* (never deleted or resolved), and unmarked if it returns. |
| `publish.comments` | `shadow` | `off`: nothing. `shadow`: render the comment only. `post`: create or update the owned summary comment. |
| `publish.suggestions` | `include` | `include`: when the model proposes a replacement for changed lines, the inline comment carries a one-click suggestion block. `off`: never. A human applies a suggestion; ChangeHarbor never writes code. |
| `publish.inline_min_severity` | `low` | Lowest severity posted inline: `info`, `low`, `medium`, `high` or `critical`. Lower findings stay in the summary comment, and their earlier inline comments are kept, not marked outdated. |
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
| `github.app` | — | Authenticate as a GitHub App instead of `token_env`. See the [GitHub guide](../guides/github.md#github-app). |
| `github.app.app_id` | required with `github.app` | Numeric App ID. |
| `github.app.private_key_env` | required with `github.app` | Variable holding the App's PEM private key (PKCS#1 or PKCS#8 RSA). |
| `github.app.installation_id` | resolved into the lock | Numeric installation ID. When omitted, `reviewd config resolve` looks it up. |

### `review`

Trusted review guidance and path filters. They come only from this file, never
from the repository under review.

```yaml
# changeharbor.yaml
version: 1
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: GITLAB_TOKEN
    mirror:
      path: /srv/mirrors/app.git
    policy: policies/app.json
    review:
      instructions: Prefer explicit error handling. Flag any new global state.
      path_instructions:
        - paths: ["migrations/**"]
          instructions: Check that every migration can be rolled back.
      exclude: ["vendor/**", "**/*.pb.go", "go.sum"]
```

| Key | Default | Meaning |
| --- | --- | --- |
| `review` | — | The section itself. Changing anything in it invalidates existing reviews. |
| `review.instructions` | — | Repository-wide instructions added to every review prompt. |
| `review.path_instructions` | — | List of `{paths, instructions}`. An entry is added only when a changed, non-excluded file (old or new path) matches one of its globs. |
| `review.exclude` | — | Globs of files left out of the model's input, such as lockfiles, vendored and generated code. Excluded files are listed in the summary and can't get comments. Risk-policy minimums still count them, so excluding can't lower risk. A file moved out of an excluded directory is still reviewed. If every changed file is excluded, nothing is reviewed: the run is `input_incomplete` and is never approved. |

All instructions together are capped at 16 KiB. Globs use `*` within a path
segment, `?` for one character and `**` for whole segments.

### `summary`

Optional background summaries of the repository, used as review context.

| Key | Default | Meaning |
| --- | --- | --- |
| `summary.model` | required | Name of a model in `models`. |
| `summary.limits` | required | Independent token and byte limits for summary generation. |

## Which changes invalidate existing reviews

A saved review can only be published while its **review identity** is
unchanged: `url`, `provider`, `api_url`, `fork_source`, `mirror.path`, `policy`
(and the policy file's contents, except `auto_approve_max`), `branch`, and
everything under `review` (`review.instructions`, `review.path_instructions`,
`review.exclude`).
Everything under `publish`, `gitlab`, `github`, tokens, `github.app` and mirror
sync settings (`mirror.sync`, `mirror.auth`, `mirror.url`) can change freely:
switching between a token and a GitHub App keeps existing reviews valid. `reviewd config check` lists the identity keys.

## The lock file

`reviewd config resolve` writes `changeharbor.lock.yaml` next to the config
with the IDs it looked up: repository and fork IDs, default branch, bot user
and, for a GitHub App, the installation ID and App slug. Commit it. It records
the settings it was resolved from. If `url`, `provider`, `api_url`,
`fork_source`, `token_env`, `bot_user_id` or anything under `github.app` change,
commands stop with *lock is stale … run reviewd config resolve*.
