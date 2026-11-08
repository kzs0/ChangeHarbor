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
| MR/PR opened or updated | Withdraw ChangeHarbor's approval of an older head, then capture from the API, sync the mirror, review, and comment and approve per `publish`. |
| Push to the configured branch | Update the change journal, then withdraw approvals whose target moved. |

Webhook payloads only *name* the change. Commits always come from the API.

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
