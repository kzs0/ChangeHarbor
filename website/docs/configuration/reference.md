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
    auth:
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

To get a default, leave the key out. An empty value (`branch: ""` or
`branch:`) is an error, never a request for the default.

Unknown keys are errors too, and the error says where and suggests a fix:
`repositories.app.gitlab.ci_trus: unknown key; did you mean "ci_trust"?`

The file is plain YAML. Anchors, aliases, merge keys (`<<`), custom tags,
duplicate keys and multiple documents are rejected. In text fields,
unquoted numbers stay as written (`bot_user_id: 12345`, `branch: 1.10`), and
`true` or `false` is an error until you quote it.

Apart from `$CHANGEHARBOR_CONFIG`, `$CHANGEHARBOR_STATE` and the secrets you
name, no environment variable changes behaviour.

## Top level

| Key | Default | Meaning |
| --- | --- | --- |
| `version` | required | Always `1`. |
| `$schema` | — | Optional schema path, for editors that prefer a key over the modeline comment. |
| `state` | `.changeharbor` beside this file | State directory: reviews, audit records, jobs. Overridden by `$CHANGEHARBOR_STATE`, then `--state`. |
| `service` | — | Needed only for [`reviewd serve`](../guides/service.md). |
| `models` | — | Named [model configs](models.md): `{name: path}` or `{name: {inline config}}`. |
| `defaults` | — | Values every repository inherits. See [`defaults`](#defaults). |
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

### `defaults`

`defaults` takes the repository keys below, and every repository inherits
them. Use it for what most repositories share: credentials, the policy,
publish modes and triggers.

How a repository's own values merge with the inherited ones:

- Mappings merge key by key, and the repository's keys win.
- Scalars and lists are replaced, never concatenated. A repository clears an
  inherited list with `[]`.
- `policy` (a path or an inline policy) and `summary.limits` are replaced as
  a whole.
- A repository that sets `auth.token_env` drops an inherited
  `auth.github_app`, and one that sets any `auth.github_app` key drops an
  inherited `auth.token_env`. Inside `auth.github_app` the keys merge, so a
  repository can add its `installation_id` to the inherited App. Likewise
  `model` and `response` replace each other.
- `gitlab` and `auth.token_scheme` apply only to GitLab repositories, and
  `github` only to GitHub repositories.
- Relative paths resolve against this file's directory, as they do under a
  repository.

These keys identify one repository and are not allowed in defaults: `url`,
`provider`, `api_url`, `fork_source`, `branch`, `mirror.path`, `mirror.url`,
`auth.bot_user_id`, `auth.bot_username` and `auth.github_app.installation_id`.
The [deprecated spellings](#deprecated-keys) aren't accepted in `defaults`
either. Errors in `defaults` name the `defaults.` path, e.g.
`defaults.publish.approvals: "yes" is not one of off, shadow, submit`.

`reviewd config check` reports inherited values with the source `defaults`.
Moving a value from a repository into `defaults`, or back, changes neither
the review identity nor the lock.

In this example `web` inherits the App, the policy and the triggers, and
posts no comments but still submits approvals:

```yaml
# changeharbor.yaml
version: 1
defaults:
  auth:
    github_app:
      app_id: 123456
      private_key_env: CHANGEHARBOR_APP_KEY
  policy: policies/default.json
  publish:
    approvals: submit
    comments: post
  triggers:
    ignore_authors: ["dependabot[bot]"]
repositories:
  api:
    url: https://github.com/acme/api
    mirror:
      path: /srv/mirrors/api.git
  web:
    url: https://github.com/acme/web
    mirror:
      path: /srv/mirrors/web.git
    publish:
      comments: shadow
```

## Repository keys

Under `repositories.<name>`:

| Key | Default | Meaning |
| --- | --- | --- |
| `url` | required | Repository web URL. Provider, web/API bases and path are derived from it. |
| `provider` | from host | `github` or `gitlab`. Needed only for self-managed hosts. |
| `api_url` | derived | API base: `https://api.github.com`, `https://HOST/api/v3` (GitHub Enterprise) or `https://HOST/api/v4` (GitLab). Override for path-prefixed installs. |
| `auth` | required | Credentials and bot identity. See [`auth`](#auth). |
| `fork_source` | — | The one fork whose MRs/PRs are accepted, e.g. `contributors/app`. |
| `branch` | default branch, pinned in the lock | Target branch reviews and approvals are for. |
| `policy` | required | [Risk policy](policy.md): a file path (JSON, or YAML when it ends in `.yaml` or `.yml`) or an inline mapping. |
| `model` | `default` if defined | Name of a model in `models`. |
| `response` | — | Saved model response for offline trials. Mutually exclusive with `model`. |

### `auth`

```yaml
auth:
  token_env: GITLAB_TOKEN     # or github_app, exactly one
  token_scheme: bearer        # GitLab only
  bot_user_id: 12345          # optional; resolved into the lock
  bot_username: reviewd-bot   # optional
```

| Key | Default | Meaning |
| --- | --- | --- |
| `auth.token_env` | required unless `auth.github_app` | Variable holding the forge token. Set exactly one of `auth.token_env` and `auth.github_app`. |
| `auth.token_scheme` | `private-token` | GitLab only: `private-token` or `bearer`. |
| `auth.github_app` | — | Authenticate as a GitHub App instead of `auth.token_env`. GitHub only. See the [GitHub guide](../guides/github.md#2-create-the-github-app). |
| `auth.github_app.app_id` | required with `auth.github_app` | Numeric App ID. |
| `auth.github_app.private_key_env` | required with `auth.github_app` | Variable holding the App's PEM private key (PKCS#1 or PKCS#8 RSA). |
| `auth.github_app.installation_id` | resolved into the lock | Numeric installation ID. When omitted, `reviewd config resolve` looks it up. |
| `auth.bot_user_id` | from the lock | The bot's numeric user ID. Resolved from `/user`, or for a GitHub App from its `<slug>[bot]` user. |
| `auth.bot_username` | from the lock | The bot's login, used for the `@<bot_username> review` comment command and the retry hint in status comments. Resolved with `auth.bot_user_id` (for a GitHub App, `<slug>[bot]`). For a GitHub App it's always resolved. With a token, if you set `auth.bot_user_id` yourself, set this too; without it only GitHub's `/changeharbor review` form works. |

### `mirror`

| Key | Default | Meaning |
| --- | --- | --- |
| `mirror.path` | required | Local git mirror. Source is read only from here. |
| `mirror.sync` | `auto` | `auto` fetches from `origin` when the mirror has it, and clones the mirror from `mirror.url` when `mirror.path` doesn't exist; `none` never fetches; any other value names a remote configured in the mirror. |
| `mirror.auth` | `token` for a GitHub App with `sync: auto`, else `none` | `none`: git uses the mirror's own configuration for credentials. `token`: clone and fetch `mirror.url` with the repository's forge token (the App installation token or `auth.token_env`), passed to git only through its environment, never in the URL, argv or on disk. Requires `sync: auto`. |
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

### `review`

Trusted review guidance, path filters and large-change handling. They come
only from this file, never from the repository under review.

```yaml
# changeharbor.yaml
version: 1
repositories:
  app:
    url: https://gitlab.com/group/app
    auth:
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
      large_changes: partial
    triggers:
      drafts: skip
      ignore_authors: ["dependabot[bot]", "renovate[bot]"]
      skip_labels: ["skip-review"]
      skip_title_keywords: ["[skip review]", "WIP"]
```

| Key | Default | Meaning |
| --- | --- | --- |
| `review` | — | The section itself. Changing anything in it invalidates existing reviews. |
| `review.instructions` | — | Repository-wide instructions added to every review prompt. |
| `review.path_instructions` | — | List of `{paths, instructions}`. An entry is added only when a changed, non-excluded file (old or new path) matches one of its globs. |
| `review.exclude` | — | Globs of files left out of the model's input, such as lockfiles, vendored and generated code. Excluded files are listed in the summary and can't get comments. Risk-policy minimums still count them, so excluding can't lower risk. A file moved out of an excluded directory is still reviewed. If every changed file is excluded, nothing is reviewed: the run is `input_incomplete` and is never approved. |
| `review.large_changes` | `incomplete` | What to do when the diff doesn't fit the model's request budget. `incomplete`: send nothing; the summary comment says the change was not reviewed and what to do. `partial`: review as many whole files as fit, in diff order, and list the rest as *Not reviewed (too large)*. A partial review is never approved, and findings on files that weren't sent are rejected. |

All instructions together are capped at 16 KiB. Globs use `*` within a path
segment, `?` for one character and `**` for whole segments.

### `triggers`

When `reviewd serve` reviews a change on its own. These keys don't affect
`reviewd review --url` or the review comment command, and changing them
invalidates nothing. Every value is read from the forge API, never from the
webhook payload.

| Key | Default | Meaning |
| --- | --- | --- |
| `triggers` | — | The section itself. |
| `triggers.drafts` | `skip` | `skip` or `review` draft pull/merge requests. A skipped draft is reviewed when it is marked ready. |
| `triggers.ignore_authors` | — | Author logins not reviewed automatically. `*` matches any run of characters; nothing else is special, so `dependabot[bot]` is literal. Case is ignored. |
| `triggers.skip_labels` | — | Labels that skip automatic review (whole name, case ignored). |
| `triggers.skip_title_keywords` | — | Title substrings that skip automatic review (case ignored), e.g. `[skip review]`. |

### `summary`

Optional background summaries of the repository, used as review context.

| Key | Default | Meaning |
| --- | --- | --- |
| `summary.model` | required | Name of a model in `models`. |
| `summary.limits` | required | Independent token and byte limits for summary generation. |

## Deprecated keys

Older files spell some keys differently. They still work, and every command
prints a warning naming the new key. Setting both spellings in one repository
is an error, and neither spelling is accepted in `defaults`.

| Old key | New key | Status |
| --- | --- | --- |
| `token_env` | `auth.token_env` | deprecated |
| `token_scheme` | `auth.token_scheme` | deprecated |
| `bot_user_id` | `auth.bot_user_id` | deprecated |
| `bot_username` | `auth.bot_username` | deprecated |
| `github.app` | `auth.github_app` | deprecated |
| `review.auto` | `triggers` | deprecated |

So `github.app.app_id`, `github.app.private_key_env` and
`github.app.installation_id` are now under `auth.github_app`, and
`review.auto.drafts`, `review.auto.ignore_authors`, `review.auto.skip_labels`
and `review.auto.skip_title_keywords` are now under `triggers`.

`reviewd config migrate --layout` prints the file rewritten to the new keys,
with comments kept, and lists each change on stderr; add `--write` to replace
the file. The lock stays fresh and existing reviews stay valid.

## Which changes invalidate existing reviews

A saved review can only be published while its **review identity** is
unchanged. These keys make it up: `url`, `provider`, `api_url`,
`fork_source`, `mirror.path`, `policy` (the path or `inline`, and the
policy's contents except `auto_approve_max`), `branch`, and everything under
`review`: `review.instructions`, `review.path_instructions`,
`review.exclude` and `review.large_changes`.

Everything else can change freely: `triggers`, `publish`, `auth` (switching
between a token and a GitHub App keeps reviews valid), `gitlab`, `github`,
`model` and the mirror sync settings (`mirror.sync`, `mirror.auth`,
`mirror.url`). Identity uses effective values, so moving a value into or out
of `defaults` changes nothing. The policy's path (or `inline`) is part of
the identity, so renaming the policy file or switching between a file and an
inline policy invalidates reviews, even when the rules are the same.
`reviewd config check` lists the
identity keys as `InvalidatesReviews` and the current identity as
`ReviewIdentity`.

## The lock file

`reviewd config resolve` writes `changeharbor.lock.yaml` next to the config
with the IDs it looked up: repository and fork IDs, default branch, bot user
ID and username and, for a GitHub App, the installation ID and App slug.
Commit it. It records the settings it was resolved from. If `url`,
`provider`, `api_url`, `fork_source`, `auth.token_env`, `auth.bot_user_id`,
`auth.bot_username` or anything under `auth.github_app` change,
commands stop with *lock is stale … run reviewd config resolve*. Only the
values count: moving them to the `auth` spelling or into `defaults` keeps
the lock fresh.

A lock written before `auth.bot_username` existed stays valid; rerun
`reviewd config resolve` to record the username and enable the `@` form of
the review command.
