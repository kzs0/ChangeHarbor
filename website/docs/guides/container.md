# Run in a container

Tagged releases publish `ghcr.io/kzs0/reviewd` for `linux/amd64` and
`linux/arm64`, tagged with the full version (`0.1.0`) and the minor version
(`0.1`). Pin the full version. The image:

- runs `reviewd` as its entrypoint, as the non-root user `reviewd`
  (UID 10001);
- keeps its working directory and home in the `/data` volume;
- includes `git` and CA certificates, and nothing that holds a secret.

[`deploy/`](https://github.com/kzs0/ChangeHarbor/tree/main/deploy) in the
repository has a Compose file that runs `reviewd serve` with Caddy in front
for TLS. This page walks through using it.

## Lay out the files

```text
changeharbor/
├── docker-compose.yml        # from deploy/
├── Caddyfile                 # from deploy/
├── reviewd.env               # secrets, mode 600, not committed
└── config/
    ├── changeharbor.yaml
    ├── changeharbor.lock.yaml
    ├── models/review.json
    └── policies/app.json
```

`config/` is mounted read-only at `/config`, so relative paths in
`changeharbor.yaml` (models, policies) resolve inside it. Point state and
mirrors at the container's volumes and listen on loopback:

```yaml
# changeharbor.yaml
version: 1
state: /data/state
service:
  address: 127.0.0.1:8080
  github_secret_env: GITHUB_WEBHOOK_SECRET
models:
  default: models/review.json
repositories:
  api:
    url: https://github.com/acme/api
    auth:
      github_app:
        app_id: 123456
        private_key_env: CHANGEHARBOR_APP_KEY
    mirror:
      path: /mirrors/api.git
    policy: policies/api.json
    publish:
      comments: post
```

With `mirror.sync: auto` (the default) the first review clones the mirror
into the `mirrors` volume, so there is nothing to clone by hand.

`reviewd.env` holds every secret the config names, one per line:

```sh
GITHUB_WEBHOOK_SECRET=...
CHANGEHARBOR_APP_KEY="-----BEGIN RSA PRIVATE KEY-----
...
-----END RSA PRIVATE KEY-----"
OPENAI_API_KEY=...
```

The private key needs its real line breaks. Docker Compose reads a
double-quoted multi-line value from an `env_file`; plain `docker run
--env-file` doesn't, so the commands below pass the key with `-e` instead.

## Resolve the lock

The lock is written next to the config, which the service mounts read-only.
Resolve it once with a writable mount, then commit both files:

```sh
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD/config:/config" \
  -e CHANGEHARBOR_APP_KEY="$(cat app.private-key.pem)" \
  ghcr.io/kzs0/reviewd:0.1.0 config resolve --config /config/changeharbor.yaml

docker run --rm -v "$PWD/config:/config:ro" \
  ghcr.io/kzs0/reviewd:0.1.0 config check --config /config/changeharbor.yaml
```

With a token instead of an App, pass the token variable the same way, for
example `-e GITLAB_TOKEN`.

Run `config resolve` again whenever you add a repository or change
`url`, `auth.token_env`, `auth.bot_user_id`, `auth.bot_username` or
`auth.github_app`.

## Start it

Edit the host name in `Caddyfile` to the name your forge will call, point
DNS at the host, and open ports 80 and 443. Then:

```sh
docker compose up -d
docker compose logs -f reviewd
curl -s https://reviewd.example.com/health
```

Caddy gets a certificate on first start. It shares the `reviewd`
container's network namespace (`network_mode: service:reviewd`), which is
why the ports are published on the `reviewd` service and why
`127.0.0.1:8080` reaches the listener.

A local model server can join the same namespace the same way
(`network_mode: service:reviewd`); then the model endpoint is
`http://127.0.0.1:PORT/...`, which ChangeHarbor accepts without TLS. A model
server anywhere else needs `https://`.

## One-off commands

The service holds the state lock, so `comment`, `publish` and `revalidate`
against the same state fail while it runs. Read-only commands work with
`docker compose exec`:

```sh
docker compose exec reviewd reviewd explain --run RUN_ID --state /data/state
docker compose exec reviewd reviewd config check --config /config/changeharbor.yaml
```

To try a change by hand without stopping the service, review it into a
separate state directory:

```sh
docker compose exec reviewd reviewd review \
  --config /config/changeharbor.yaml --state /data/scratch \
  --url https://github.com/acme/api/pull/7
docker compose exec reviewd reviewd publish \
  --config /config/changeharbor.yaml --state /data/scratch --run RUN_ID
```

That costs a model call, and nothing is written to the forge while the
repository's modes are `shadow`.

## Without Compose

The service listens on `127.0.0.1` inside its container, so publishing port
8080 doesn't reach it. Run the proxy in the same network namespace, as the
Compose file does. Here `reviewd.env` holds everything except the private
key, which is passed with `-e`:

```sh
docker run -d --name reviewd --restart unless-stopped \
  --env-file reviewd.env -e CHANGEHARBOR_APP_KEY="$(cat app.private-key.pem)" \
  -v "$PWD/config:/config:ro" -v reviewd-state:/data -v reviewd-mirrors:/mirrors \
  -p 80:80 -p 443:443 \
  ghcr.io/kzs0/reviewd:0.1.0 serve --config /config/changeharbor.yaml

docker run -d --name caddy --restart unless-stopped --network container:reviewd \
  -v "$PWD/Caddyfile:/etc/caddy/Caddyfile:ro" -v caddy-data:/data \
  caddy:2.10
```

Or run the binary on the host under [systemd](service.md#keep-it-running).

## Upgrades and backups

See [Operating ChangeHarbor](../operations/operating.md#upgrades) for
upgrades, backups and what to watch in the logs.
