# GitLab contract

Bounded GitLab REST v4 adapters. All requests use numeric project IDs, never
follow redirects, cap response bytes and return `*StatusError` for non-2xx.
`NewClient` sends `PRIVATE-TOKEN`; `NewBearerClient` sends
`Authorization: Bearer`; an empty token sends no credential.

- `Change`: MR identity, state, draft (`draft` or legacy `work_in_progress`),
  target branch, head pipeline ID, diff base, and the **live** target branch tip.
- `Collect(Expect) Facts`: one live snapshot of MR, token identity
  (`/user`), project merge settings, approval settings, protected branch,
  MR approval state, head pipeline, merged-result parents and pipeline jobs.
  403/404 from Premium approval endpoints are recorded as unknown, not errors.
- `Evaluate(Facts, Expect)`: returns passed required jobs or one withholding
  reason. Approval requires all of:
  - head/target unchanged, MR open and not draft, target branch equals mapping,
    GitLab diff base equals the locally computed merge base;
  - token acts as `bot_user_id`;
  - pipelines must succeed, skipped pipelines not mergeable, and target
    freshness enforced natively (`ff`, `rebase_merge`, or merged results);
  - approvals reset on push (selective code-owner removal is insufficient);
  - target protected and force push disallowed;
  - every unapproved rule with required approvals can be completed by the bot
    alone (eligible or `any_approver`, at most one approval left);
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
