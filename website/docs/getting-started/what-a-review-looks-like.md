# What a review looks like

This page shows the comments ChangeHarbor writes, as the forge receives
them. They come from the same renderers the binary uses, and the test suite
checks this page against them. The `<!-- reviewd:... -->` lines are HTML
comments: the forge hides them, and ChangeHarbor uses them to find and
update its own comments.

On each change you get, at most:

- one **summary comment**, created on the first review and edited in place
  after that;
- one **inline comment** per finding, on the changed line it's about, with
  `publish.inline_comments: post`;
- an **approval** of the exact reviewed commit, with
  `publish.approvals: submit` and only when every requirement holds.

## The summary comment

The header lines are host facts: the commit, the status and the risk
scores. Everything the model wrote sits inside the `text` code block, so it
can't mention people, render links or run GitLab quick actions.

````markdown
<!-- reviewd:summary github:1234:7 -->
<!-- reviewd:run run_abc123 -->
### reviewd review (advisory)

Reviewed commit: `4f9a1c2e8b7d` · Status: `completed` · Merge risk: model 3/10, enforced minimum 2/10, effective 3/10, threshold 3/10

Approval is decided separately by host policy; this comment never approves or merges.

```text
Code review
Status: completed
Local advisory eligibility: true
Merge risk: model 3/10, enforced minimum 2/10, effective 3/10; approval threshold 3/10
Summary: Adds a token cache with retries.
Risk rationale: Small, contained change with tests.
Prompt digest: sha256:9f2c…
Coverage complete: true
Coverage: diff: included
Coverage: file:internal/auth/token.go: included
Coverage: excluded:go.sum: excluded_by_trusted_policy
Excluded by trusted policy: go.sum
Finding [medium/correctness]: Retry loop never gives up
Trigger: fetchToken retries on every error, including 401.
Consequence: A revoked token makes the worker spin forever.
Source: internal/auth/token.go:42
Evidence: diff
Walkthrough:
internal/auth/token.go: Caches the token and retries failed fetches.
internal/auth/token_test.go: Covers expiry and retry.
Advisory report only. No approval submission or merge performed.
```
````

Reading it from the top:

- `Merge risk` gives the model's score, the minimum your
  [risk policy](../configuration/policy.md) enforced, the effective risk
  (the higher of the two) and your approval threshold.
- `Coverage` lines say what went into the prompt. Files left out by
  `review.exclude` are listed as `Excluded by trusted policy`.
- Each `Finding` has a severity and category, what triggers the problem,
  what happens if it ships, and the line it's on.
- `Walkthrough` has one line per changed file, when the model provides it.
- Any `Unresolved question` or `Limitation` the model reports is listed at
  the end. An unresolved question withholds approval, and so does any
  `high` or `critical` finding, whatever the risk score.

## Inline comments

Each finding on a changed line gets its own comment. On GitHub they're
posted together as one review with the `COMMENT` event; on GitLab each is a
diff discussion.

````markdown
<!-- reviewd:inline github:1234:7 343fdb1aac9d72cc -->
<!-- reviewd:run run_abc123 -->
**reviewd** · severity `medium` · `correctness`

```text
Retry loop never gives up
Trigger: fetchToken retries on every error, including 401.
Consequence: A revoked token makes the worker spin forever.
```

```suggestion
if resp.StatusCode == http.StatusUnauthorized {
	return "", errRevoked
}
```
````

The second marker field is the finding's fingerprint (path, category and
title), so the same finding on a later push updates its comment instead of
adding a new one.

### Suggestions

When the model proposes replacement code for the changed lines, and
`publish.suggestions` is `include` (the default), the comment ends with a
suggestion block that a reviewer can apply in one click. ChangeHarbor never
commits it.

A suggestion that replaces several lines is anchored to all of them. GitHub
gets a multi-line review comment with a plain ` ```suggestion ` block.
GitLab uses its line-range syntax instead, for example ` ```suggestion:-0+2 `
for three lines. On GitLab a suggestion with a line starting with `/` is
replaced by a note, because that line could run a quick action.

### Outdated comments

When a later review no longer reports a finding, its comment is kept and a
notice is put on top. If the finding comes back, the notice is removed.

```markdown
<!-- reviewd:outdated -->
**Possibly outdated: not reported by the latest review.**

<!-- reviewd:inline github:1234:7 343fdb1aac9d72cc -->
...
```

Findings below `publish.inline_min_severity` stay in the summary only, and
comments they had from earlier reviews are left alone.

## Partial reviews

With `review.large_changes: partial`, a change too big for the model's
request budget is reviewed file by file, in diff order, until the budget is
used up. The summary says so above the report, and lists the files that
were left out:

````markdown
### reviewd review (advisory)

Reviewed commit: `4f9a1c2e8b7d` · Status: `partial` · Merge risk: model 3/10, enforced minimum 2/10, effective 3/10, threshold 3/10

Partial review: 1 changed file(s) were too large for the model's request budget and were not reviewed; they are listed in the report. Approval is withheld for partial reviews.

Approval is decided separately by host policy; this comment never approves or merges.

```text
...
```
````

Inside the report, each skipped file appears as
`Not reviewed (too large): testdata/big.json`, and the status line reads
`Withholding reason: files_omitted_for_size`. A partial review is never
approved, and comments on files that weren't reviewed aren't marked
outdated.

## When a review doesn't complete

If there is no review to show, the summary comment becomes a status comment.
It names a reason code and the run ID, never an error message or model
output. No approval is given and inline comments are left as they were. The
next review that completes replaces it.

````markdown
<!-- reviewd:summary github:1234:7 -->
<!-- reviewd:run run_abc123 -->
### reviewd review (advisory): not completed

Commit: `4f9a1c2e8b7d` · Status: `input_incomplete` · Reason: `required_input_omitted`

This change was not reviewed. The diff is too large for the model's request budget, so nothing was sent to the model.

What to do: split the change, leave generated or vendored files out with `review.exclude`, set `review.large_changes: partial` to review as many files as fit, or use a model with a larger context window.

No approval was given. Inline comments from earlier reviews were left unchanged.
````

The other reasons use the same layout:

| Reason | Status | The comment says |
| --- | --- | --- |
| `required_input_omitted` | `input_incomplete` | This change was not reviewed. The diff is too large for the model's request budget, so nothing was sent to the model. |
| `all_changes_excluded_by_trusted_policy` | `input_incomplete` | This change was not reviewed. Every changed file matches `review.exclude`, so there was nothing to send to the model. |
| `invalid_output` | `invalid_output` | The model's reply was rejected. It did not pass validation (for example, a finding pointed outside the changed lines), so none of it is shown here. |
| `model_request_failed` | `review_failed` | The review could not be completed. The model request failed or timed out. |
| `mirror_unavailable` | `review_failed` | The review could not be completed. The local mirror could not provide the change's commits. |
| `internal_error` | `review_failed` | The review could not be completed. An internal error occurred. |

For the last four, *What to do* reads "comment `@changeharbor review` to try
again, or push a new commit" (with your bot's username), or "push a new
commit to try again" when the username isn't known. It also tells operators
where to look: `reviewd explain --run RUN_ID` for rejected output, the run ID
for the others.

`review_failed` comments come only from the service, after the job's last
attempt fails, and only with `publish.comments: post`.

## Approvals

The approval itself carries no review text. On GitHub it's a review with
the `APPROVE` event, the body `reviewd: validated host policy approval` and
the reviewed commit as `commit_id`. On GitLab it's an approval with the
reviewed `sha`. When the change or its target moves, ChangeHarbor withdraws
it: GitHub dismisses the review, GitLab unapproves.
