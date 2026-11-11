# Operating ChangeHarbor

## What lives where

| Path | Holds | Back it up? |
| --- | --- | --- |
| `changeharbor.yaml`, its lock, policies, model configs | What you want reviewed and how | Keep them in version control. |
| `state` directory | `state.db` (reviews, model requests and replies, audit records), `jobs.db` (the webhook queue), artifacts | Yes, with `reviewd backup`. |
| Mirrors | Git mirrors of each repository | No. With `mirror.sync: auto` a missing mirror is cloned again. |
| Secrets | Tokens, the App key, webhook secrets, model keys | In your secret store; ChangeHarbor only reads them from the environment. |

## Health

`GET /health` answers once the service is listening. It reports each
repository's write modes, keyed by provider and the forge's numeric
repository or project ID:

<!-- example: health -->
```json
{
  "status": "ok",
  "processing": "durable_worker",
  "repositories": {
    "github:812345678": {"approvals": "shadow", "comments": "post", "inline_comments": "post"},
    "gitlab:9": {"approvals": "submit", "comments": "post", "inline_comments": "shadow"}
  }
}
```

The modes are read when the service starts, so restart it after changing
`publish`. A repository whose legacy JSON config can't be loaded reports
`unknown`. The payload names no paths, URLs or variables, so it's safe to
expose through the proxy.

`/health` doesn't check the forge, the model or the mirrors. Use it to see
that the process is up and running the modes you expect.

## Logs

Everything goes to stderr. Under systemd that's the journal
(`journalctl -u changeharbor -f`); in a container, `docker compose logs -f
reviewd`. These are the lines worth watching for:

| Line | Means |
| --- | --- |
| `saved forge review: RUN_ID` | A review finished and was saved. Comments and approval follow, per `publish`. |
| `skipped automatic review of github:812345678:42: draft` | `triggers` skipped the change. The reason is `draft`, `ignored_author`, `skip_label` or `skip_title_keyword`. |
| `tracked-branch reconciliation incomplete; will retry` | The periodic check of the target branch didn't finish, usually because the forge or the mirror was unreachable. One is noise; a run of them needs a look. |
| `scheduled context build incomplete; inspect audit records` | Rebuilding the repository context snapshot failed. Reviews still run. |
| JSON lines with `"msg":"operation.complete"` | Telemetry for background operations. `"success":false` marks a failure; error text is deliberately left out. |

A job that fails doesn't log its error. With `publish.comments: post` its
change gets a [status comment](../getting-started/what-a-review-looks-like.md#when-a-review-doesnt-complete)
after the last attempt, and the job is marked `failed` in the queue. To list
failed jobs on a host with `sqlite3`:

```sh
sqlite3 /var/lib/changeharbor/jobs.db \
  "select change_id, kind, attempts from queue_jobs where state = 'failed'"
```

Events for pull or merge requests into another branch, and pushes to
branches other than `branch`, are accepted and ignored; they don't show up
as failures. An event for a repository that isn't in the file still ends as
`failed` without touching the forge.

Each failed attempt also writes one line to the service's stderr with the
job, its kind, the change, the attempt number, the run ID (when a review was
started) and a short reason. For a review, the `change_id` is
`provider:repository:number`. To see more, review that change by hand into a
scratch state directory, which works while the service runs and prints the
error on stderr:

```sh
reviewd review --url https://github.com/acme/api/pull/42 --state /tmp/changeharbor-debug
```

## Several repositories

One file holds every repository and one service serves them all. GitHub and
GitLab repositories can share a file; each forge needs its own webhook
secret. Put what they share under `defaults`; each repository inherits it
and sets only what differs.

```yaml
# changeharbor.yaml
version: 1
state: /var/lib/changeharbor
service:
  address: 127.0.0.1:8080
  github_secret_env: GITHUB_WEBHOOK_SECRET
  gitlab_secret_env: GITLAB_WEBHOOK_SECRET
models:
  default: models/review.json
  small: models/review-small.json
defaults:
  auth:
    github_app:
      app_id: 123456
      private_key_env: CHANGEHARBOR_APP_KEY
  publish:
    comments: post
  triggers:
    ignore_authors: ["dependabot[bot]", "renovate[bot]"]
repositories:
  api:
    url: https://github.com/acme/api
    mirror:
      path: /srv/mirrors/api.git
    policy: policies/api.json
    publish:
      inline_comments: post
  web:
    url: https://github.com/acme/web
    mirror:
      path: /srv/mirrors/web.git
    policy: policies/web.json
    model: small
    review:
      exclude: ["dist/**", "package-lock.json"]
  billing:
    url: https://gitlab.com/acme/billing
    auth:
      token_env: GITLAB_TOKEN    # replaces the inherited App
      bot_username: changeharbor-bot
    mirror:
      path: /srv/mirrors/billing.git
      auth: token
    policy: policies/billing.json
    publish:
      approvals: submit          # merged with the inherited comments: post
```

Mappings such as `publish` merge with the inherited ones, while a list such
as `review.exclude` replaces the inherited list. A repository that sets
`auth.token_env` drops the inherited App. See
[`defaults`](../configuration/reference.md#defaults) for the full rules.

Each repository can appear only once, and each reviews changes into one
`branch` (the default branch unless you set it). One GitHub App installation
can serve every repository it's installed on; each repository still needs
its own entry. After adding one, run `reviewd config resolve` and restart the
service.

A webhook for a repository that isn't in the file is accepted, and its job
fails without doing anything. Subscribe only the repositories you've
configured; with a GitHub App, install it only on those.

## Upgrades

1. Read the release notes.
2. Take a backup (below).
3. Replace the binary and restart: `systemctl restart changeharbor`, or
   change the image tag and run `docker compose up -d`.
4. Run `reviewd config check` and look at `/health`.

Webhooks that arrive while the service is down are lost unless the forge
redelivers them. GitHub lists failed deliveries under the webhook's
**Recent deliveries** and can redeliver them; GitLab retries some failures
itself. A push or a review comment brings any change up to date.

Locks written by older versions stay valid. If the release added something
the lock records (as `auth.bot_username` was added), rerun `reviewd config
resolve` to fill it in.

## Backups

```sh
reviewd backup --state /var/lib/changeharbor --output /backups/changeharbor-$(date +%F).tar
```

Each database is copied with a consistent snapshot, so this works while the
service runs. For one point in time across both databases, stop the service
first. The output's directory must be writable; a temporary directory is
created next to it.

To restore, use an empty directory, then point `state` at it:

```sh
reviewd restore --input /backups/changeharbor-2026-11-09.tar --state /var/lib/changeharbor-restored
```

Restore verifies every artifact and both databases before it finishes.
