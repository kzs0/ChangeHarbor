# Run the service

`reviewd serve` receives webhooks and runs the whole flow for each change:
review, comment, approve. Add a `service` section to the same file:

```yaml
# changeharbor.yaml
version: 1
state: /var/lib/changeharbor
service:
  address: 127.0.0.1:8080
  gitlab_secret_env: GITLAB_WEBHOOK_SECRET
  github_secret_env: GITHUB_WEBHOOK_SECRET
  reconciliation_interval: 5m
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

```sh
reviewd serve --config /etc/changeharbor/changeharbor.yaml
```

## What happens on each event

| Event | Action |
| --- | --- |
| MR/PR opened, reopened, new commits, marked ready, or retargeted | Withdraw ChangeHarbor's approval of an older head, apply [`review.auto`](#automatic-reviews), then capture from the API, sync the mirror, review, and comment and approve per `publish`. |
| `@<bot_username> review` comment (GitHub also `/changeharbor review`) | The same, but `review.auto` is bypassed. See [Ask for a review](#ask-for-a-review). |
| Push to the configured branch | Update the change journal, then withdraw approvals whose target moved. |

Other MR/PR events (labels, title or description edits, approvals) don't start
a review. Webhook payloads only *name* the change or the comment. Commits,
draft state, labels, authors and comment text always come from the API.

## Automatic reviews

By default drafts are skipped. `review.auto` adds more rules:

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
      auto:
        drafts: skip
        ignore_authors: ["dependabot[bot]", "renovate*"]
        skip_labels: ["skip-review"]
        skip_title_keywords: ["[skip review]"]
```

A skipped change gets no review, no comment and no approval; stale approvals
are still withdrawn. The service logs
`skipped automatic review of gitlab:9:7: draft` and records it in state.
Removing a skip label or keyword takes effect on the next push, or ask for a
review.

## Ask for a review

A comment whose first line is exactly `@<bot_username> review` reviews the
change's current head again, drafts and skip rules included. On GitHub,
`/changeharbor review` also works. Later lines are ignored, so you can add a
reason. The comment's author needs write access: GitHub owner, member or
collaborator, or GitLab Developer or above. Other comments, and the bot's own,
are ignored silently; every decision is recorded in state as
`review_command`. A command is never dropped in favour of a newer event for
the same change.

The bot's username comes from `bot_username` or the lock (`reviewd config
resolve`; for a GitHub App it records `<app-slug>[bot]`). Subscribe the
webhook to comments: GitHub *Issue comments*, GitLab *Comments*.

## When a review doesn't complete

With `publish.comments: post` the owned summary comment says what happened,
why and what to do, for example that the diff is too large for the model's
request budget or the model's reply was rejected. It shows only a reason code
(such as `required_input_omitted` or `model_request_failed`) and the run ID,
never error text. If the model call or the mirror fails, the comment is left
after the job's last attempt. Nothing is approved, and inline comments are left
as they were. The next completed review replaces the comment.

For large changes, `review.large_changes: partial` reviews as many whole files
as fit and lists the rest. The request budget comes from the model config
(`limits.max_request_bytes`, context and token caps, cost cap), so a larger
model takes larger changes.

## Expose it safely

`serve` only listens on loopback. Put a TLS reverse proxy in front and forward
only `/webhooks/` and `/health`:

```caddyfile
changeharbor.example.com {
    reverse_proxy /webhooks/* 127.0.0.1:8080
    reverse_proxy /health 127.0.0.1:8080
}
```

Webhooks are authenticated by GitHub's HMAC signature or GitLab's secret
token. Receipts are stored durably before they are acknowledged.

`/health` reports each repository's write modes, for example
`{"status":"ok","processing":"durable_worker","repositories":{"gitlab:9":{"approvals":"submit","comments":"post","inline_comments":"shadow"}}}`.
A repository whose configuration can't be loaded reports `unknown`. The modes
are read when the service starts; restart it after changing the configuration.

## Keep it running

```ini
# /etc/systemd/system/changeharbor.service
[Unit]
Description=ChangeHarbor
After=network-online.target

[Service]
User=changeharbor
EnvironmentFile=/etc/changeharbor/secrets.env
ExecStart=/usr/local/bin/reviewd serve --config /etc/changeharbor/changeharbor.yaml
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

`secrets.env` holds the tokens, webhook secrets and model key named in the
config.

## One writer per state directory

The service holds `<state>/writer.lock`. While it runs, `publish`, `comment`
and `revalidate` against the same state fail with *another reviewd instance
holds the state lock*. `doctor` and `config check` still work. Run one service
per state directory.

## Backups

```sh
reviewd backup --state /var/lib/changeharbor --output /backups/changeharbor.tar
reviewd restore --input /backups/changeharbor.tar --state /var/lib/changeharbor-restored
```

Restore needs an empty destination and verifies every artifact and the SQLite
databases.
