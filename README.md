# ChangeHarbor / reviewd

Self-hosted Go code review and release assessment. Deterministic host code gathers
pinned source and evidence, sends one batched model request, validates output, and
enforces risk floors. The model has no tools. Approval defaults to shadow.

The authoritative [v3 specification](docs/specification.html), [plan](docs/PLAN.md),
[status and operational boundaries](docs/STATUS.md), and
[dependency inventory](docs/dependencies.md) are included.

## Build and verify

Requires Go 1.27.3, just, Git and a C compiler for SQLite. CI reads the Go
version from `go.mod`.

```sh
cd /workspace/reviewd
. scripts/env.sh
just check build
```

Elsewhere, use `just check build` with Go 1.27.3 on PATH, or
`just go=/path/to/go check build`. Tests use local Git, fake models and
loopback HTTP fixture servers; no paid credentials or external telemetry needed.
Restricted sandboxes must permit loopback listeners for HTTP tests.

## Local workflow

```sh
bin/reviewd context build --repo /trusted/repo --ref HEAD --state /data/reviewd
bin/reviewd journal reconcile --repo /trusted/repo --branch main \
  --through HEAD --state /data/reviewd
bin/reviewd review --repo /trusted/repo --base BASE --head HEAD \
  --response testdata/review-response.json --state /data/reviewd > review.json
bin/reviewd assess --run review.json --threshold 3 --state /data/reviewd
bin/reviewd explain --run RUN_ID --state /data/reviewd
bin/reviewd evaluate --fixtures testdata/evaluation.json
```

Commands print JSON to stdout and durable run IDs/telemetry to stderr. `assess`,
`explain`, rendering and evaluation make no live model calls. Fake responses are
explicit fixtures, not real semantic assessments. Exit codes: 0 complete,
1 operational failure, 2 invalid input/configuration, 3 configured threshold or
fixture mismatch, 4 incomplete scope/assessment.

## Live model and forge review

Replace `--response` with `--model-config /trusted/model.json`. The HTTP adapter
speaks OpenAI-compatible structured chat completions. Configure explicit model
capabilities, token/byte/cost/deadline caps and a credential environment-variable
name. Request intent, attempts, exact responses and usage are retained locally;
uncertain completion is never silently replayed. Unknown usage/price remains
unknown. See [HTTP contract](internal/model/CONTRACT.md).

```sh
bin/reviewd review --url https://github.com/OWNER/REPO/pull/123 \
  --forge-config /trusted/forge.json --model-config /trusted/model.json \
  --state /data/reviewd
bin/reviewd publish --run RUN_ID --forge-config /trusted/forge.json \
  --state /data/reviewd
```

Forge configuration binds web/API host, provider repository IDs, permitted fork
source ID, local mirror, policy and trusted CI producer IDs. Local mirrors must
contain the captured commits. The default publish mode is shadow. Submission
requires supported live protection/check facts and matching saved provenance;
unknown native/human requirements withhold approval. No auto-merge occurs.

### Review comments

```sh
bin/reviewd comment --run RUN_ID --forge-config /trusted/forge.json \
  --state /data/reviewd
```

Posts or updates one summary comment per PR/MR, owned by `bot_user_id` and
identified by a stable `<!-- reviewd:summary ... -->` marker. Comments by any
other author are never edited, even if they contain the marker. Off by
default: set `"comment_mode": "post"` (and `bot_user_id`) to write; otherwise
the command prints the body. Posting is withheld when the PR head or target
moved since the review. Intent is recorded before writing and ambiguous writes
are reconciled by listing comments before any retry. Model-derived text is
placed in a code fence, with control characters removed and line-leading `/`
indented, so it cannot trigger GitLab quick actions, mentions or markup.
GitHub identity is verified from the write response (App installation tokens
cannot call `/user`); GitLab tokens are verified up front.

### GitLab submission

GitLab forge configuration uses `project_id` (equal to `repository_id`),
`repository` (the web path, e.g. `group/app`), `bot_user_id` (numeric user the
token acts as; required for `submit`), optional `token_scheme`
(`private-token` default, or `bearer`) and `required_checks` mapping job names
to `project:<project_id>`. Mirrors must fetch
`+refs/merge-requests/*/head:refs/merge-requests/*/head` and the target branch.

`ci_trust` is required for GitLab approval: `external_config` (recommended)
requires `ci_config_path` to load the pipeline from another project;
`unchanged_config` withholds MRs that touch `.gitlab-ci.yml`, a local
`ci_config_path` or any `ci_paths` glob. Optional `sync_remote` names a remote
configured in the mirror; ChangeHarbor then fetches the target branch and MR
head before reviewing and before every publication check.

```sh
bin/reviewd doctor --forge-config /trusted/forge.json      # read-only preflight
bin/reviewd revalidate --forge-config /trusted/forge.json --state /data/reviewd
```

`doctor` checks token identity/scope/expiry, the bot's role, project merge and
approval settings, branch protection, approval rules, CI trust and the mirror.
`revalidate` withdraws ChangeHarbor approvals whose MR head or target moved
since approval; `serve` runs it after each push to the target branch. With a
`forge_config` in a `serve` repository mapping, MR webhooks run the whole flow
(API-captured review, comment, publish) per that configuration's modes. One
process may write a state directory at a time (`<state>/writer.lock`).

Approval is submitted only when live GitLab facts establish: MR open, not draft
and targeting the configured branch; pipelines must succeed with fast-forward,
semi-linear or merged results pipelines; approvals reset on push; protected
target without force push; approval rules completable by the bot; and a
successful head pipeline in the target project, bound to the reviewed head (or
a merged-results commit of exactly the live target and head), containing every
required job. Premium approval endpoints are required: on GitLab Free the run is
withheld. Definite provider refusals are audited as withheld and may be retried;
ambiguous outcomes stay pending for reconciliation.

## Release assessment

```sh
bin/reviewd release assess --repo /trusted/repo --baseline v1 --candidate v2 \
  --branch main --model-config /trusted/release-model.json \
  --policy /trusted/release-policy.json --state /data/reviewd > release.json
bin/reviewd release render --assessment release.json --format html > release.html
bin/reviewd release render --assessment release.json --format draft > draft.md
```

Operational evidence is supplied through `--operational-evidence` and
`--trusted-producers`, pinned to the candidate and optional `--build-id`.
Missing required evidence stays incomplete. New live release outputs use
classified evidence-backed items; legacy offline fixtures remain labelled.
The channel output is a draft and is never sent.

## Service and recovery

`reviewd serve --config /trusted/service.json` exposes authenticated GitHub/GitLab
webhooks on an explicit loopback address. Durable receipt precedes acknowledgement.
Workers use fenced leases, heartbeats and bounded retry counts. Periodic local-Git
reconciliation catches missed events independently of review success; scheduled
context rebuilds use durable leases. Optional background summaries require a
separate model configuration and explicit independent limits.

```sh
bin/reviewd backup --state /data/reviewd --output /backups/reviewd.tar
bin/reviewd restore --input /backups/reviewd.tar --state /data/restored-reviewd
```

Restore requires a new destination and verifies artifacts and SQLite integrity.
See [operations](docs/OPERATIONS.md) for backup consistency and release packaging.
