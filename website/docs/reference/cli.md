# CLI

All commands print JSON to stdout and progress (such as `saved forge review:
RUN_ID`) to stderr.

**Exit codes:** `0` done · `1` operational error · `2` invalid input or
configuration · `3` threshold or fixture mismatch · `4` withheld, incomplete or
needs attention.

Commands that read `changeharbor.yaml` accept `--config FILE` and
`--state DIR`.

## Setup

| Command | Does |
| --- | --- |
| `reviewd config init --url URL --mirror PATH --policy FILE --token-env NAME` | Writes a commented `changeharbor.yaml`, its schema and lock. Never overwrites. Also `--provider`, `--api-url`, `--name`. |
| `reviewd config resolve` | Looks up IDs from the forge and writes `changeharbor.lock.yaml`. |
| `reviewd config check` | Validates and prints the effective config (including `auth`, `github_app_id`, `github_installation_id`, `mirror_auth` and `policy_digest`), where each value came from (`file`, `defaults`, `default`, `lock`, `policy`), the keys that invalidate reviews, each repository's `ReviewIdentity` and any deprecation `Warnings`. |
| `reviewd config schema` | Prints the JSON Schema. |
| `reviewd config migrate --forge-config FILE [--server-config FILE]` | Converts legacy JSON configs to `changeharbor.yaml` and a lock, offline. |
| `reviewd config migrate --layout [--config FILE] [--write]` | Rewrites deprecated keys (`token_env`, `github.app`, `review.auto`, …) to the current layout, keeping comments. Prints the new file and the changes; `--write` replaces the file. Offline; the lock stays fresh. |
| `reviewd doctor [--repo NAME]` | Read-only GitLab preflight: token, role, CI trust, mirror and forge protections. Protection gaps are errors, or warnings with `publish.protections: advisory`. |

## Reviewing and publishing

| Command | Does |
| --- | --- |
| `reviewd review --url MR_OR_PR_URL` | Reviews a change captured from the forge API. Also `--model-config`/`--response` to override the configured model. |
| `reviewd comment --run RUN_ID` | Creates or updates the owned summary comment (per `publish.comments`) and the inline comments on changed lines (per `publish.inline_comments`). |
| `reviewd publish --run RUN_ID` | Approves, or reports would-approve/would-withhold (per `publish.approvals`). |
| `reviewd revalidate [--repo NAME]` | Withdraws ChangeHarbor's approvals whose MR/PR head or target moved (GitLab unapprove, GitHub review dismissal). |
| `reviewd serve --config FILE` | Runs the webhook service. See [Run the service](../guides/service.md). |

## Local and offline tools

| Command | Does |
| --- | --- |
| `reviewd review --repo PATH --base REV --head REV` | Reviews a local revision range (advisory only; can't be published). |
| `reviewd assess --run RUN_ID --threshold N` | Re-evaluates a saved review against a threshold, without a model call. |
| `reviewd explain --run RUN_ID` | Shows how a saved run's decision was reached. Reads the state named by `--state`, `CHANGEHARBOR_STATE` or the config (`--config`, `CHANGEHARBOR_CONFIG`, `./changeharbor.yaml`). |
| `reviewd context build --repo PATH --ref REV` | Builds a repository context snapshot. |
| `reviewd journal reconcile --repo PATH --branch NAME --through REV` | Records merged changes into the change journal. |
| `reviewd release assess --repo PATH --baseline REV --candidate REV` | Assesses a release candidate. `reviewd release render` renders the result as markdown, HTML or a channel draft. |
| `reviewd evaluate --fixtures FILE` | Replays offline invariants. |
| `reviewd backup --state DIR --output FILE` | Consistent backup of the state directory. |
| `reviewd restore --input FILE --state NEW_DIR` | Verified restore into an empty directory. |

## Environment

| Variable | Used for |
| --- | --- |
| `CHANGEHARBOR_CONFIG` | Config path when `--config` isn't given. |
| `CHANGEHARBOR_STATE` | State directory override (below `--state`). |
| *(names you configure)* | Forge tokens (`auth.token_env`), GitHub App private keys (`auth.github_app.private_key_env`), webhook secrets and model keys. |
