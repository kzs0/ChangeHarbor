# ChangeHarbor

ChangeHarbor reviews merge requests and pull requests with a language model,
**explains every decision**, and only approves a change when it can prove
its evidence holds. It runs on your machine, reads code
from a local mirror, and never merges anything.

<div class="grid cards" markdown>

- **New here?** Read [How it works](getting-started/how-it-works.md), then
  try the [Quickstart](getting-started/quickstart.md). It takes ten
  minutes and writes nothing to your forge.
- **Setting up a repository?** Follow the [GitLab](guides/gitlab.md) or
  [GitHub](guides/github.md) guide, step by step.
- **Configuring?** Everything lives in one commented
  [`changeharbor.yaml`](configuration/reference.md).
- **Something withheld?** The [troubleshooting](operations/troubleshooting.md)
  table maps every reason to a fix.

</div>

## What it does

1. **Reviews** a change in one model call, with the full diff and selected
   context, and returns findings plus a merge risk score from 1 to 10.
2. **Enforces your risk policy.** Path, size and familiarity rules set a
   minimum risk the model can't lower.
3. **Comments** one summary on the MR/PR and keeps it up to date.
4. **Approves** only when every requirement holds on the live forge:
    - the risk is at or below your threshold;
    - checks passed on the reviewed commit;
    - nothing moved since the review.

    When the change moves later, ChangeHarbor withdraws its own approval.

Every write is off by default. In the default `shadow` mode ChangeHarbor
evaluates everything and tells you what it *would* do.

## What it never does

- Merge, deploy or post anywhere except the MR/PR it reviewed.
- Edit a comment it didn't write.
- Run code from the repository, or give the model tools.
- Approve on stale evidence: a moved head, a moved target or a changed policy
  all withhold.
