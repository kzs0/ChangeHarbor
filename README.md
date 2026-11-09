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
speaks OpenAI-compatible structured chat completions (`api: openai_chat`, the
default) or the Anthropic Messages API (`api: anthropic_messages`); provider
recipes are at https://kzs0.github.io/ChangeHarbor/configuration/providers/.
Configure explicit model
capabilities, token/byte/cost/deadline caps and a credential environment-variable
name. Request intent, attempts, exact responses and usage are retained locally;
uncertain completion is never silently replayed. Unknown usage/price remains
unknown. See [HTTP contract](internal/model/CONTRACT.md).

### Configuration

Full documentation: https://kzs0.github.io/ChangeHarbor/

One reviewed file, `changeharbor.yaml`, configures every repository, and a
committed `changeharbor.lock.yaml` pins identities resolved from the forge API
(design: [docs/design/config.md](docs/design/config.md)).

```sh
bin/reviewd config init --url https://gitlab.com/group/app \
  --mirror /mirrors/app.git --policy policies/app.json --token-env GITLAB_TOKEN
bin/reviewd config check          # effective values, where each came from
bin/reviewd doctor                # live GitLab preflight
```

```yaml
# changeharbor.yaml
version: 1
state: /data/changeharbor
models:
  default: models/review.json
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: GITLAB_TOKEN
    mirror:
      path: /mirrors/app.git
    policy: policies/app.json
    publish:
      approvals: submit
      comments: post
```

- Provider, web/API bases and path come from `url`. Project ID, default
  branch, bot user and fork source are resolved by `reviewd config resolve`
  into the lock; changing their inputs makes the lock stale and commands
  refuse to run until it is resolved again.
- Omitted keys use safe defaults: `publish.approvals` `shadow` (evaluate every
  requirement, write nothing), `publish.comments` `shadow` (render only),
  `mirror.sync` `auto` (fetch from `origin` when configured),
  `gitlab.ci_trust` `external_config`, `gitlab.required_jobs` from the policy,
  `branch` from the lock. Empty strings are errors. `off` disables a feature
  entirely.
- Relative paths resolve against the config file. `--config`, then
  `$CHANGEHARBOR_CONFIG`, then `./changeharbor.yaml` locates it; `--state`, then
  `$CHANGEHARBOR_STATE`, then the file sets the state directory. Nothing else is
  read from the environment except named secrets.
- Only review identity (url, provider, api_url, fork_source, mirror path,
  policy, branch) invalidates existing reviews; `config check` lists it.
- `reviewd config schema` prints the JSON Schema; `config init` writes it next
  to the config for editor completion.
- Legacy `--forge-config` files still work for one release with a warning;
  `reviewd config migrate --forge-config F [--server-config S]` converts them
  offline and keeps existing reviews publishable.

```sh
bin/reviewd review --url https://gitlab.com/group/app/-/merge_requests/123
bin/reviewd publish --run RUN_ID
bin/reviewd comment --run RUN_ID
bin/reviewd serve --config changeharbor.yaml   # needs a "service" section
```

Local mirrors must contain the captured commits (or set `mirror.sync`).
Approvals are submitted when `publish.approvals` is `submit`, ChangeHarbor's
own evidence holds and, by default, the forge's protections are in place.
`publish.protections: advisory` (opt-in) approves regardless of protection
settings. Either way ChangeHarbor withdraws its own approval when the change or
its target moves.
No auto-merge occurs.

### Review comments

```sh
bin/reviewd comment --run RUN_ID
```

Posts or updates one summary comment per PR/MR, owned by the bot user (from
the lock, or `bot_user_id`) and identified by a stable
`<!-- reviewd:summary ... -->` marker. Comments by any other author are never
edited, even if they contain the marker. Set `publish.comments` to `post` to
write; the default `shadow` prints the body. Posting is withheld when the PR head or target
moved since the review. Intent is recorded before writing and ambiguous writes
are reconciled by listing comments before any retry. Model-derived text is
placed in a code fence, with control characters removed and line-leading `/`
indented, so it cannot trigger GitLab quick actions, mentions or markup.
GitHub identity is verified from the write response (App installation tokens
cannot call `/user`); GitLab tokens are verified up front.

With `publish.inline_comments: post`, the same command also posts one comment
per finding on its changed line (GitLab diff discussions; one GitHub COMMENT
review). Findings are identified by a fingerprint of path, category and title,
so re-runs and new pushes never duplicate them; a finding the latest review no
longer reports has its comment marked "possibly outdated" (never deleted or
resolved) and unmarked if it returns. The summary includes a per-file
walkthrough when the model provides one.

`review.instructions` and `review.path_instructions` add trusted review
guidance to the prompt, and `review.exclude` leaves generated or vendored files
out of the model's input (risk-policy minimums still count them).
`publish.suggestions` (default `include`) adds one-click suggestion blocks to
inline comments, and `publish.inline_min_severity` (default `low`) keeps
lower findings in the summary only. Design:
[docs/design/review-guidance-and-suggestions.md](docs/design/review-guidance-and-suggestions.md).

### GitLab submission

GitLab repositories may set `token_scheme` (`private-token` default, or
`bearer`) and a `gitlab` section. `gitlab.required_jobs` defaults to the
policy's `required_checks`. Without `mirror.sync`, mirrors must fetch
`+refs/merge-requests/*/head:refs/merge-requests/*/head` and the target branch
themselves.

`gitlab.ci_trust` decides whether the head pipeline's definition is trusted:
`external_config` (default, recommended) requires `ci_config_path` to load the
pipeline from another project; `unchanged_config` withholds MRs that touch
`.gitlab-ci.yml`, a local `ci_config_path` or any `gitlab.ci_paths` glob;
`none` always withholds. With `mirror.sync`, ChangeHarbor fetches the target
branch and MR head before reviewing and before every publication check.

```sh
bin/reviewd doctor                 # read-only preflight, every GitLab repository
bin/reviewd revalidate             # withdraw approvals whose MR moved
```

`doctor` checks token identity/scope/expiry, the bot's role, CI trust, the
mirror and the forge protections (errors; protection gaps are warnings with
`publish.protections: advisory`). `revalidate` withdraws ChangeHarbor approvals
(GitLab unapprove, GitHub review dismissal) whose head or target moved; `serve`
runs it for each MR/PR event before reviewing, and after each target push. Under
`serve`, MR webhooks run the whole flow (API-captured review, comment, publish)
per each repository's `publish` settings. One
process may write a state directory at a time (`<state>/writer.lock`).

Approval is submitted when live GitLab facts establish: MR open, not draft
and targeting the configured branch; the token is the bot; CI trust holds; a
successful head pipeline in the target project, bound to the reviewed head (or
a merged-results commit of exactly the live target and head), contains every
required job; and, unless `publish.protections: advisory`, pipelines must
succeed with fast-forward/semi-linear/merged-results merges, approvals reset on
push, the target is protected without force push, and approval rules are
completable by the bot (these need GitLab Premium). Definite provider refusals are audited as withheld and may be retried;
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
