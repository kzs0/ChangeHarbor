# Security model

ChangeHarbor's job is to approve less than a careless human would, never
more. The rules on this page are enforced in code, not by convention.

## Trust boundaries

ChangeHarbor trusts `changeharbor.yaml`, its lock, the risk policy and the
model configs, all files an operator reviews, plus live facts it reads from
the forge API.

It doesn't trust anything in the change: code, commit messages, PR
descriptions, comments and CI output. Webhook payloads aren't trusted
either; they only name a change or a comment, and everything else is read
from the API.

Model output is untrusted input too. It's validated against a schema, and it
can raise the risk but never lower the policy's minimum.

## What it enforces

Nothing from the repository runs. Source is read with git plumbing, with
hooks, external diffs and text conversion disabled, and the model has no
tools.

Approvals are bound to the reviewed commit (`commit_id` on GitHub, `sha` on
GitLab). On every MR/PR event, before reviewing a new head, and after every
push to the target branch, ChangeHarbor unapproves (GitLab) or dismisses its
review (GitHub) wherever the head or target moved since it approved. It does
this even when the forge keeps approvals on push.

By default an approval also requires the forge's own protections: approval
reset, a protected branch and merge gates. A repository opts out only by
setting `publish.protections: advisory`. Before every write ChangeHarbor
re-reads the forge and withholds if the head, the target, the policy or the
configuration changed.

A check counts only if a trusted producer ran it on the reviewed commit. On
GitLab the pipeline definition itself must be out of the author's reach
(`gitlab.ci_trust`); on GitHub each required check is tied to the app that
produces it (`github.required_checks`).

Comments can't act. Model text is posted inside a code block, with
line-leading `/` indented and control characters removed, so it can't
trigger GitLab quick actions, mentions or markup. Status comments contain
only reason codes and validated host facts. ChangeHarbor never edits a
comment someone else wrote.

Review commands need write-level access (GitHub owner, organization member
or collaborator; GitLab Developer or above), and the command only causes a
review that pushing a commit would have caused anyway. The bot's own
comments never trigger it.

Every write is recorded before it's sent. An unclear outcome is reconciled
by reading the forge, never retried blindly. The same goes for model
requests: one whose outcome is unknown is never replayed.

## Credentials and privilege

Tokens and secrets come only from the environment variables you name.
GitHub App JWTs and installation tokens, which are scoped to one repository,
stay in memory: they're never logged or written to the state directory, the
lock or the mirror. Git receives a token only through its environment, as a
header scoped to `mirror.url`, with credential helpers and redirects
disabled.

`doctor` warns when the GitLab bot can merge. Telemetry stays local: logs go
to stderr and nothing is exported.

## Reporting a vulnerability

Please open a private security advisory on the GitHub repository.
