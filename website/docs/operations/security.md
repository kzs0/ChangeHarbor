# Security model

ChangeHarbor's job is to approve less than a careless human would, never
more. These are the rules it enforces in code.

## Trust boundaries

- **Trusted:** `changeharbor.yaml`, its lock, the risk policy and model
  configs (operator-reviewed files), and live facts from the forge API.
- **Untrusted:** everything in the change: code, commit messages, PR
  descriptions, comments and CI output. Also webhook payloads, which only
  *name* a change.
- **Model output** is untrusted input too. It is validated against a schema
  and can raise the risk, but it never lowers the policy minimum.

## What it enforces

- **No execution.** Source is read with git plumbing, with hooks, external
  diffs and text conversion disabled. The model has no tools.
- **Revision-bound approvals.** Approvals carry the reviewed commit
  (`commit_id` on GitHub, `sha` on GitLab).
- **ChangeHarbor withdraws its own approvals.** On every MR/PR event, before
  reviewing a new head, and after every target push, ChangeHarbor unapproves
  (GitLab) or dismisses its review (GitHub) wherever the head or target moved
  since it approved. Approvals don't depend on forge protection settings, so
  this holds even when the forge keeps approvals on push.
- **Live re-checks.** Before every write, ChangeHarbor re-reads the forge and
  withholds if the head, target, policy or configuration changed.
- **Trusted CI.** A check counts only if a trusted producer ran it on the
  reviewed commit. On GitLab, the pipeline definition itself must be out of
  the author's reach (`gitlab.ci_trust`).
- **Comments that can't act.** Model text is posted inside a code block, with
  line-leading `/` indented and control characters removed, so it can't
  trigger GitLab quick actions, mentions or markup. Comments by anyone else
  are never edited.
- **Durable intent.** Every write is recorded before it's sent. An unclear
  outcome is reconciled, never blindly retried.
- **Least privilege.** `doctor` warns when the bot can merge. Tokens and
  secrets come only from the environment variables you name. Telemetry export
  is off by default.

## Reporting a vulnerability

Please open a private security advisory on the GitHub repository.
