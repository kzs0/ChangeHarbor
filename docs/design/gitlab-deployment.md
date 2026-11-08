# GitLab deployment hardening

Designs closing the deployment gaps found after GitLab approval publication
and comments landed. Each item is implemented test-first. Supporting older
self-managed GitLab versions is out of scope: missing fields keep failing
closed.

## 1. CI configuration trust

**Problem.** For same-project MRs the head pipeline runs the MR's own
`.gitlab-ci.yml`. An author can redefine a required job (`test: script: true`)
and the trusted-pipeline check still passes.

**Design.** New forge config `ci_trust`, required for GitLab approval:

| Mode | Requirement checked live |
| --- | --- |
| `external_config` | project `ci_config_path` is `file@group/project[:ref]` naming a **different** project. MR authors cannot change it. |
| `unchanged_config` | the reviewed diff touches no CI configuration: `.gitlab-ci.yml`, a local `ci_config_path`, and every `ci_paths` glob (e.g. `.gitlab/ci/**`, included templates, scripts jobs call). |
| empty | approval withheld: "CI configuration trust is not configured". |

`external_config` is the recommended setting. Protecting the ref of the
external project is the operator's job; `doctor` reports it. `unchanged_config`
relies on the operator listing every path that defines job behaviour.
`ci_trust`/`ci_paths` are publication policy and are excluded from the review
config digest.

## 2. Webhook-driven automation

**Problem.** `serve` reviews MR webhooks from payload revisions into local runs
with no forge provenance, so nothing it produces can be commented or approved.

**Design.** A repository mapping may name a `forge_config`. When it does, a
review job for an MR runs the full flow:

1. `review --url` core: capture identity and revisions from the **API** (the
   payload only supplies the MR number), sync the mirror, review, save
   provenance.
2. `comment` when `comment_mode` is `post`.
3. `publish` when `mode` is `submit`.

The event's repository ID must equal the forge mapping's `repository_id`. A
push to the configured target branch runs ingestion, then approval
revalidation (item 6). Shadow settings keep the whole flow write-free.

## 3. Mirror synchronization

**Problem.** ChangeHarbor never fetches, so a webhook can arrive before an
external sync has fetched the MR head, and a stale local target branch
withholds every approval.

**Design.** Optional `sync_remote`: the **name** of a remote already
configured in the mirror (never a URL from config or a payload). When set,
ChangeHarbor runs a hardened `git fetch` before reviewing and before every
publication freshness check:

- GitLab: `+refs/heads/<branch>:refs/heads/<branch>` and
  `+refs/merge-requests/<iid>/head:refs/merge-requests/<iid>/head`
- GitHub: `+refs/heads/<branch>:refs/heads/<branch>` and
  `+refs/pull/<n>/head:refs/pull/<n>/head`

The fetch disables hooks, runs with `--no-tags`, never prompts, and validates
the remote name, branch and refspecs. Credentials come from the mirror's own
repository configuration.

## 4. Deployment preflight (`reviewd doctor`)

**Problem.** Bot token scope, role and project settings are misconfigured
silently and only show up later as withheld approvals.

**Design.** `reviewd doctor --forge-config F` (GitLab) reports named checks
with `error` or `warning` severity and exits 4 on any error:

- `token_identity`: `/user` equals `bot_user_id`
- `token_scopes`: the active token includes `api`; a warning if it expires
  within 14 days
- `project_role`: Developer is required; Maintainer or above gets a warning
  (it can merge, which is broader than needed)
- `pipelines_required`, `skipped_pipelines`, `target_freshness`, and a
  `merge_trains` warning
- `approval_reset`, which requires Premium
- `branch_protection`
- `approval_rules`: a warning for each required rule the bot cannot satisfy
- `ci_trust`
- `mirror`: the configured branch resolves locally, and a sync succeeds when
  `sync_remote` is set

## 5. Fork merge requests

**Problem.** Fork MR pipelines run in the fork project and are correctly
untrusted, but the withholding reason did not say why or what to do.

**Design.** When the head pipeline ran in the MR's source (fork) project, the
reason says so and asks a maintainer to run the pipeline in the parent
project. Trust rules are unchanged.

## 6. Approval revalidation

**Problem.** ChangeHarbor never withdraws an approval. With merge-commit
merges, the target can move without a push to the MR, and the approval then
covers a combination that was never reviewed.

**Design.**
- Each published GitLab approval is indexed under `active_approvals/<repo>`
  with the run, head and target.
- `reviewd revalidate --forge-config F` re-reads each indexed MR:
  - merged or closed: the entry is dropped;
  - head or target changed: if the bot is still listed as an approver, POST
    `/unapprove` (404 counts as already revoked), then drop the entry;
  - unchanged: the entry is kept.
- `serve` runs revalidation after each push to the target branch.
- Unapproving is idempotent, so a retry is always safe.

## 7. Single-writer state

**Problem.** State is single-host SQLite and concurrent publishers are
unsupported, but nothing enforced that.

**Design.** An exclusive non-blocking `flock` on `<state>/writer.lock`.
`serve`, `publish`, `comment` and `revalidate` hold it for their
lifetime (`doctor` is read-only and does not); a second holder fails with "another reviewd instance holds the
state lock". Inside `serve` the automated flow runs under the service's lock
rather than taking it again.

## Update: approvals no longer depend on protections

Approvals are now submitted whenever `publish.approvals` is `submit` and
ChangeHarbor's own evidence holds, on GitLab and GitHub, whatever the forge's
protection settings and on any GitLab tier. The protection checks moved to
`doctor` as warnings. To keep approvals from outliving the reviewed commit
where the forge keeps approvals on push, revalidation (item 6) also runs for
the changed MR/PR on every MR/PR event, *before* the new head is reviewed, and
covers GitHub by dismissing ChangeHarbor's review.
