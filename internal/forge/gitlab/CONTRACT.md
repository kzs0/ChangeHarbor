# GitLab contract

Bounded GitLab REST v4 adapters. All requests use numeric project IDs, never
follow redirects, cap response bytes and return `*StatusError` for non-2xx.
`NewClient` sends `PRIVATE-TOKEN`; `NewBearerClient` sends
`Authorization: Bearer`; an empty token sends no credential.

- `Change`: MR identity, state, draft (`draft` or legacy `work_in_progress`),
  target branch, head pipeline ID, diff base, and the **live** target branch tip.
- `Collect(Expect) Facts`: one live snapshot of MR, token identity
  (`/user`), project settings, head pipeline, merged-result parents and
  pipeline jobs; plus, when `Expect.Protections` is required (default),
  approval settings, protected branch and MR approval state (403/404 from
  Premium endpoints recorded as unknown). Advisory reads no protection
  setting and no Premium-only endpoint.
- `Evaluate(Facts, Expect)`: returns passed required jobs or one withholding
  reason. Approval requires all of:
  - head/target unchanged, MR open and not draft, target branch equals mapping,
    GitLab diff base equals the locally computed merge base;
  - token acts as `bot_user_id`;
  - unless protections are advisory: pipelines must succeed, skipped
    pipelines not mergeable, target freshness enforced natively, approvals
    reset on push, target protected without force push, and every open
    approval rule completable by the bot alone;
  - CI configuration trust (`ci_trust`) holds;
  - head pipeline in the target project, source `merge_request_event` or
    `push`, status `success`, and bound to the reviewed revision: either its SHA
    is the head, or it is `refs/merge-requests/<iid>/merge` whose parents are
    exactly `[live target, head]`;
  - each required job name occurs exactly once in that pipeline (latest
    attempts), with status `success`, and is mapped to `project:<project id>`.
- `Approve(project, iid, head, bot)`: POST with `sha` fence. Success requires the
  bot in `approved_by` (GitLab's `approved` means fully approved). 401 re-reads
  approvals to reconcile "already approved". 401 without approval, 403, 404,
  409 and 422 are `ErrRejected` (definitely not written); 5xx and transport
  errors are ambiguous. The remote ID is `gitlab:<project>:<iid>:<head>:<bot>`.
- `Comments` lists MR notes excluding system notes. `CreateNote`/`UpdateNote`
  return the written note with its author; 401/403/404/422 are `ErrRejected`,
  other failures ambiguous.
- `Diagnose`: preflight. Errors for token identity, `api` scope, Developer
  role, CI trust and, unless advisory, forge protections; with advisory
  protections, protection gaps are warnings.
- `Unapprove` (404 = already withdrawn) and `ApprovedBy` support revalidation,
  which withdraws ChangeHarbor's approval whenever the MR head or target moved.
- `Discussions` lists MR discussions (first note, system notes skipped).
  `CreateDiscussion(project, iid, body, Position)` starts a diff discussion
  positioned with `base_sha`, `start_sha` (`Change.StartOID`), `head_sha`,
  paths and exactly one of `new_line`/`old_line`; 401/403/404/422 are
  `ErrRejected`, a response without a note is ambiguous.
