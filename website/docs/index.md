# ChangeHarbor

ChangeHarbor reviews GitHub pull requests and GitLab merge requests with a
language model you choose. It posts a summary with a per-file walkthrough
and comments on the changed lines, with suggestions a reviewer can apply in
one click. It can also approve a change, but only the exact commit it
reviewed, and only when it can show that your risk policy and the forge's
own protections allow it. It runs on your host, reads code from a local
mirror, and never merges anything.

## Where to start

New to it? [How it works](getting-started/how-it-works.md) explains the
moving parts, and [What a review looks like](getting-started/what-a-review-looks-like.md)
shows the comments it posts. The [Quickstart](getting-started/quickstart.md)
reviews one merge request in ten minutes and writes nothing to the forge.

Replacing CodeRabbit? [Coming from CodeRabbit](guides/coming-from-coderabbit.md)
maps each feature and translates your `.coderabbit.yaml`.

Setting up for real, follow the [GitHub](guides/github.md) or
[GitLab](guides/gitlab.md) guide, pick a [model provider](configuration/providers.md),
run the [service](guides/service.md) or the [container](guides/container.md),
and work through the [rollout checklist](guides/rollout.md).

Everything is configured in one [`changeharbor.yaml`](configuration/reference.md).
When something is withheld, [Troubleshooting](operations/troubleshooting.md)
maps each reason to a fix.

## What it does

1. Reviews a change in one model call, with the full diff, your
   instructions and related source, and gets back findings, a per-file
   walkthrough and a merge risk score from 1 to 10.
2. Applies your risk policy. Path, size and author-familiarity rules set a
   minimum risk the model can't lower.
3. Comments: one summary per change, edited in place, and one comment per
   finding on its line. When a finding goes away, its comment is marked
   *possibly outdated*; nothing is duplicated across pushes. When a review
   can't complete, the summary says why and what to do.
4. Approves only when every requirement holds on the live forge: the risk
   is at or below your threshold, checks passed on the reviewed commit, the
   forge's protections are in place (unless you opt out with
   `publish.protections: advisory`), and nothing moved since the review.
   When the change moves later, ChangeHarbor withdraws its approval.

The service reviews changes as they're opened and updated, skips drafts and
whatever else `triggers` says to skip, and reviews again when someone
comments `@<bot_username> review`.

Every write is off by default. In `shadow` mode ChangeHarbor evaluates
everything and tells you what it would do.

## What it never does

- Merge, deploy, or post anywhere except the change it reviewed.
- Edit a comment it didn't write.
- Run code from the repository, or give the model tools.
- Take instructions from the repository under review: its config lives
  outside it.
- Approve on stale evidence. A moved head, a moved target or a changed
  policy all withhold.
