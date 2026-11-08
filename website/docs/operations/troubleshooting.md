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
| merge request targets a different branch than the trusted mapping | Retarget it, or configure that branch as its own repository entry. |
| credential does not act as the configured bot user | The token belongs to someone else. Run `reviewd config resolve` after changing tokens. |

Forge protection settings (merge method, pipelines must succeed, approval
reset, protected branches, approval rules) never withhold an approval.
`reviewd doctor` reports them as warnings.

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

## Risk

| Reason | Fix |
| --- | --- |
| host risk policy or independent requirements withhold approval | The effective risk is above `auto_approve_max`, a high/critical finding was raised, the model left questions open, or coverage was incomplete. Read the review. This is ChangeHarbor working as intended. |

## Errors (not withholdings)

| Message | Fix |
| --- | --- |
| lock is stale (… changed); run reviewd config resolve | You changed a setting the lock depends on. Run `reviewd config resolve`. |
| no configured repository matches URL | Add the repository to `changeharbor.yaml`, or check the URL. |
| another reviewd instance holds the state lock | The service is running against this state directory. |
| prior write requires reconciliation | A previous approval attempt for this run had an unknown outcome (for example a timeout), so ChangeHarbor won't retry it blindly. Check the MR: if the approval is there, nothing else is needed. Otherwise review again; the new run publishes independently. |
| ambiguous owned summary comments; reconcile manually | Two comments from the bot carry the summary marker. Delete one. |
