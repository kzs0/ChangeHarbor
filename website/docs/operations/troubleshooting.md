# Troubleshooting

When ChangeHarbor doesn't approve, it says why. In `shadow` mode the reason
appears as `would withhold: …`, and in `submit` mode as the `Reason` of a
`withheld` result (exit code `4`). Find the reason below.

Run `reviewd doctor` first on GitLab. It catches most setup problems before a
review does.

## Revisions and freshness

| Reason | What it means | Fix |
| --- | --- | --- |
| reviewed HEAD or target changed | Someone pushed, or the target branch moved, after the review. | Nothing. The next webhook reviews the new commits. By hand: review again. |
| target is not an ancestor of reviewed HEAD | The MR/PR is behind its target. | Rebase or merge the target into it. |
| configured branch identity differs from live target | The mirror's target branch is stale. | Use `mirror.sync: auto`, or sync the mirror. |
| GitLab merge request diff is not computed against the live target | GitLab hasn't refreshed the MR diff since the target moved (common with stacked MRs). | Push to the MR, or wait for GitLab to refresh it. |
| local mirror … does not contain captured commit | The mirror is missing the MR/PR head. | Use `mirror.sync`, or fetch `refs/merge-requests/*/head` (GitLab) / `refs/pull/*/head` (GitHub). |

## Merge request state

| Reason | Fix |
| --- | --- |
| merge request is not open | Nothing. Merged and closed MRs are never approved. |
| merge request is a draft | Mark it ready. |
| merge request targets a different branch than the trusted mapping | Retarget it. Each repository entry reviews changes into one `branch`. |
| credential does not act as the configured bot user | The token belongs to someone else. Run `reviewd config resolve` after changing tokens. |

## Forge protections

These withhold only with the default `publish.protections: required`. Fix the
setting, or opt out with `publish.protections: advisory`.

| Reason | Setting |
| --- | --- |
| pipelines are not required to succeed before merge | **Settings → Merge requests → Pipelines must succeed** |
| skipped pipelines may be merged | Turn off **Skipped pipelines are considered successful** |
| target freshness is not enforced natively | Use fast-forward or semi-linear merges, or enable merged results pipelines. |
| approval settings or rules unavailable; GitLab Premium … | Required protections need GitLab Premium or Ultimate. |
| approvals are not reset on push | **Approval settings → When a commit is added: Remove all approvals** |
| target branch is not protected / allows force push | **Settings → Repository → Protected branches** |
| native approval rule "…" requires additional human approval | The bot can't complete that rule alone. Humans approve, or add the bot to the rule. |
| native strict protections or required human review evidence are not established (GitHub) | Turn on **Require branches to be up to date** and don't require approving reviews. |
| native required check has no verified trusted producer mapping (GitHub) | Map every required status check in `github.required_checks`. |

## Pipelines and checks

| Reason | Fix |
| --- | --- |
| CI configuration trust is not configured | Set `gitlab.ci_trust`. See the [GitLab guide](../guides/gitlab.md#3-make-the-pipeline-definition-trustworthy). |
| CI configuration is not loaded from a separate project | Set **CI/CD configuration file** to `file@group/other-project`, or use `ci_trust: unchanged_config`. |
| merge request changes CI configuration (…); human review required | Expected with `unchanged_config`: CI changes need a human. |
| merge request has no head pipeline | Run MR pipelines (`rules: - if: $CI_PIPELINE_SOURCE == "merge_request_event"`). |
| head pipeline ran in the fork project … | A maintainer runs the MR pipeline in the parent project. |
| head pipeline source or project is not trusted | Pipelines started by API, trigger tokens or the web UI aren't trusted. Push or update the MR. |
| head pipeline is not bound to the reviewed HEAD and live target | The pipeline ran for another commit. Merge train pipelines can't be bound yet. |
| head pipeline has not succeeded | Wait for it, or fix it. |
| required trusted job "…" is missing / failed or did not run / is ambiguous | Check the job name in `gitlab.required_jobs` (or the policy's `required_checks`). Parallel jobs are named `test 1/3`. |
| required check failed or has an untrusted producer/revision (GitHub) | Check the app ID in `github.required_checks`. |

## Reviews that don't complete or don't start

| What you see | What it means | Fix |
| --- | --- | --- |
| Status comment, reason `required_input_omitted` | The diff doesn't fit the model's request budget, so nothing was sent. | Split the change, exclude generated or vendored files with `review.exclude`, set `review.large_changes: partial`, or use a model with a larger context window. |
| Status comment, reason `invalid_output` | The model's reply failed validation. Nothing from it is shown. | Comment `@<bot_username> review` or push. Inspect the run with `reviewd explain --run <id> --state <state dir>`. |
| Status comment, reason `model_request_failed`, `mirror_unavailable` or `internal_error` | The job's last attempt failed. | Reproduce it with `reviewd review --url URL --state /tmp/debug`, which prints the error (the service doesn't log job errors; see [Operating](operating.md#logs)). Fix it, then comment `@<bot_username> review` or push. |
| partial review: some files were not reviewed; approval requires a complete review | `review.large_changes: partial` left files out. | Expected: partial reviews are never approved. Split the change or use a larger model. |
| review did not complete (…) | The run is `input_incomplete` or `invalid_output`. | See the status comment's reason above. |
| `skipped automatic review of …: draft` (or `ignored_author`, `skip_label`, `skip_title_keyword`) in the service log | `triggers` skipped the change. | Mark it ready, remove the label or keyword and push, or comment `@<bot_username> review`. |
| A review command does nothing | It was ignored; the reason is in state under `review_command` (`unauthorized`, `not_a_command`, `bot_author`, `bot_username_unknown`, `branch_mismatch`, …). | The command must be the comment's first line, from someone with write access (GitHub Write, Maintain or Admin role; GitLab Developer+). Set `auth.bot_username` or rerun `reviewd config resolve` for the `@` form, and subscribe the webhook to comments. |
| A GitLab MR edit doesn't start a review | Only new commits, marking ready and retargeting do. | Push, or comment `@<bot_username> review`. |

## Webhooks

The forge's delivery log (GitHub: the App's or repository's **Recent
deliveries**; GitLab: the webhook's **Recent events**) shows the status code
ChangeHarbor returned.

| Status | Meaning | Fix |
| --- | --- | --- |
| 202 | Accepted and queued (or deliberately ignored, like a comment that isn't a command). | Nothing. If no review follows, see [Logs](operating.md#logs). |
| 401 `invalid signature` / `invalid token` | The webhook secret doesn't match `github_secret_env` / `gitlab_secret_env`. | Set the same secret on both sides and restart the service. |
| 400 `delivery identity required` or `invalid event` | The request lacks the forge's delivery ID header, or its payload is missing the repository or change. | Make sure the proxy forwards headers unchanged and the content type is JSON. |
| 503 `webhook not configured` | No secret is configured for this forge. | Set `service.github_secret_env` or `service.gitlab_secret_env` and restart. |
| 503 `receipt could not be committed` | The state directory couldn't be written. | Check disk space and permissions on the state directory. |
| 413 | The payload is over 1 MiB, which can happen for very large pushes. | The next push or review command catches up. |
| 405 | The request wasn't a `POST`. | Check the webhook configuration. |
| 404 | The proxy forwarded a path other than `/webhooks/github` or `/webhooks/gitlab`. | Check the payload URL. |
| 502 or a timeout | The proxy can't reach the service. | Check that `reviewd serve` is running and listening on `service.address`. |

A delivery that was accepted but never produced a review usually belongs to
a repository or branch that isn't configured; those jobs fail without
touching the forge.

## Risk

| Reason | Fix |
| --- | --- |
| host risk policy or independent requirements withhold approval | The effective risk is above `auto_approve_max`, a high/critical finding was raised, the model left questions open, or coverage was incomplete. Read the review: this is the policy doing its job. |

## Errors (not withholdings)

| Message | Fix |
| --- | --- |
| lock is stale (… changed); run reviewd config resolve | You changed a setting the lock depends on. Run `reviewd config resolve`. |
| no configured repository matches URL | Add the repository to `changeharbor.yaml`, or check the URL. |
| review status `input_incomplete`: all_changes_excluded_by_trusted_policy | Every changed file matches `review.exclude`, so there was nothing to review. ChangeHarbor won't approve unreviewed changes; narrow the exclusions or review by hand. |
| review status `invalid_output`: finding outside canonical changed lines / suggestion outside new-side changed lines | The model pointed a finding or suggestion at a line that isn't a changed line of the reviewed diff, or at a file excluded by `review.exclude`. The run publishes nothing; review again. |
| another reviewd instance holds the state lock | The service is running against this state directory. |
| prior write requires reconciliation | A previous approval attempt for this run had an unknown outcome (for example a timeout), so ChangeHarbor won't retry it blindly. Check the MR: if the approval is there, nothing else is needed. Otherwise review again; the new run publishes independently. |
| ambiguous owned summary comments; reconcile manually | Two comments from the bot carry the summary marker. Delete one. |
