# Coming from CodeRabbit

ChangeHarbor covers the part of CodeRabbit most teams lean on: a summary
with a per-file walkthrough, inline findings with one-click suggestions,
path-specific instructions and filters, and control over which pull
requests get reviewed. It runs on your own host against a model you choose.

It does fewer things. There is no chat, no learnings, no linters and no
issue-tracker integration. In exchange it can approve a change, and it
approves only the exact commit it reviewed, only when your risk policy and
the forge's own protections allow it.

## Feature by feature

| CodeRabbit | ChangeHarbor | Notes |
| --- | --- | --- |
| High-level summary and walkthrough | Summary comment with a per-file walkthrough | One comment, edited in place on each push. ChangeHarbor never edits the PR description. See [What a review looks like](../getting-started/what-a-review-looks-like.md). |
| Inline review comments | `publish.inline_comments: post` | One comment per finding on its changed line. A finding that disappears is marked *possibly outdated*, never deleted or resolved. |
| Committable suggestions | `publish.suggestions: include` (the default) | Single- and multi-line. A human applies them. |
| `reviews.path_instructions` | `review.path_instructions` | A list of globs per entry instead of one `path`. |
| `reviews.path_filters` (`!pattern`) | `review.exclude` | Exclusions only; write the patterns without `!`. Excluded files still count toward risk-policy minimums. |
| `tone_instructions`, review profile | `review.instructions` | Free text, added to every review prompt. |
| `reviews.auto_review.enabled` | Webhook subscriptions | The service reviews every change it gets events for. |
| `reviews.auto_review.drafts: false` | `review.auto.drafts: skip` (the default) | A skipped draft is reviewed when marked ready. |
| `reviews.auto_review.ignore_title_keywords` | `review.auto.skip_title_keywords` | Case-insensitive substrings. |
| `reviews.auto_review.labels: ["!label"]` | `review.auto.skip_labels` | Skip labels only; there's no "review only with this label". |
| Skipping bot authors | `review.auto.ignore_authors` | `*` wildcards, e.g. `renovate*`. |
| `auto_review.base_branches` | `branch` | One target branch per repository, the default branch unless set. A repository can appear only once in the file. |
| `@coderabbitai review` | `@<bot_username> review`, or `/changeharbor review` on GitHub | Must be the comment's first line, from someone with write access. Bypasses `review.auto`. |
| `@coderabbitai full review`, `pause`, `resume`, `ignore`, `resolve` and other commands | Not supported | `review` is the only command. Use `review.auto.skip_labels` to pause a PR. |
| Chat (replying to the bot) | Not supported | Replies to ChangeHarbor's comments are ignored. |
| Learnings and knowledge base | Not supported | Put durable guidance in `review.instructions` instead; it's reviewed like any other config change. |
| `request_changes_workflow` | Revision-bound approvals | ChangeHarbor never requests changes. It approves the reviewed commit when the effective risk is at or below your threshold, checks passed, and nothing moved, and withdraws its approval when anything does. See [How it works](../getting-started/how-it-works.md). |
| Repository config in `.coderabbit.yaml` | `changeharbor.yaml` outside the reviewed repository | A pull request can't change how it is reviewed. |

## Translate your config

A typical `.coderabbit.yaml`:

```yaml
# .coderabbit.yaml
tone_instructions: "Be brief. Prefer explicit error handling."
reviews:
  profile: chill
  high_level_summary: true
  path_filters:
    - "!vendor/**"
    - "!**/*.pb.go"
    - "!go.sum"
  path_instructions:
    - path: "migrations/**"
      instructions: "Check that every migration can be rolled back."
    - path: "internal/auth/**"
      instructions: "Look for missing authorization checks."
  auto_review:
    enabled: true
    drafts: false
    ignore_title_keywords: ["WIP", "[skip review]"]
    labels: ["!skip-review"]
chat:
  auto_reply: true
```

The same intent in `changeharbor.yaml`. The parts with no equivalent
(`profile`, `chat`) are dropped, and the parts CodeRabbit sets up for you
(model, mirror, policy, webhook secret) are added:

```yaml
# changeharbor.yaml
version: 1
state: /var/lib/changeharbor
service:
  address: 127.0.0.1:8080
  github_secret_env: GITHUB_WEBHOOK_SECRET
models:
  default: models/review.json
repositories:
  api:
    url: https://github.com/acme/api
    github:
      app:
        app_id: 123456
        private_key_env: CHANGEHARBOR_APP_KEY
    mirror:
      path: /srv/mirrors/api.git
    policy: policies/api.json
    review:
      instructions: Be brief. Prefer explicit error handling.
      path_instructions:
        - paths: ["migrations/**"]
          instructions: Check that every migration can be rolled back.
        - paths: ["internal/auth/**"]
          instructions: Look for missing authorization checks.
      exclude: ["vendor/**", "**/*.pb.go", "go.sum"]
      auto:
        drafts: skip
        skip_title_keywords: ["WIP", "[skip review]"]
        skip_labels: ["skip-review"]
        ignore_authors: ["dependabot[bot]", "renovate[bot]"]
    publish:
      comments: post
      inline_comments: post
      approvals: shadow
```

A few things behave differently from what you may expect:

- Globs are stricter. `*` stays within one path segment, `**` spans
  segments, and braces, brackets and `!` are rejected. `**/*.pb.go` and
  `vendor/**` work as written.
- Every changed file must fit in one model request. If a pull request is too
  big, nothing is sent and the summary comment says so, unless you set
  `review.large_changes: partial`. See [Model providers](../configuration/providers.md#how-big-a-change-fits).
- Changing anything under `review` except `review.auto` makes earlier
  reviews unpublishable, so open PRs are reviewed afresh on their next push
  or command.
- Approvals start in `shadow`. Leave them there until the
  [rollout checklist](rollout.md) says otherwise.

## Running both side by side

You can run ChangeHarbor next to CodeRabbit while you compare them. Keep
ChangeHarbor's comments in `shadow` and read what it would post with
`reviewd comment --run RUN_ID`, or turn on `publish.comments: post` for a
few repositories. The two bots don't react to each other: ChangeHarbor
ignores comments that aren't its exact review command.

## What's missing

Compared with CodeRabbit today, ChangeHarbor has no:

- chat, follow-up questions or replies to its comments;
- learnings, or memory of past reviews beyond the change itself;
- incremental reviews: every review covers the whole diff against the
  target, though re-reviewing an unchanged commit reuses the earlier reply;
- linters, static analysis or security scanners; it never runs code from the
  repository;
- issue-tracker context (Jira, Linear, linked issues);
- generated docstrings, unit tests, sequence diagrams or PR titles;
- edits to the PR description, or resolution of its own threads;
- "request changes" reviews;
- reviews of pull requests into more than one target branch per repository;
- Bitbucket, Azure DevOps or Gitea support;
- per-repository config inside the repository (by design);
- `doctor` checks for GitHub: `reviewd doctor` checks GitLab projects only.

If you rely on one of these, keep that tool for it. ChangeHarbor only
writes to its own summary comment, its own inline comments and its own
approval, so it doesn't get in the way of other bots.
