# Review feedback, partial reviews and triggers

This closes the rollout gaps found on top of PR #11: silent outcomes, no
review at all for large changes, no control over what triggers a review, no
way to ask for a re-review from the change, and a misleading `/health`.

Untouched rules: repository content, PR/MR text and comments are untrusted;
webhook payloads only name a change (or a comment); nothing here can make an
approval easier to get.

## 1. Status comment when a review doesn't complete

`automateChange` returned early for any status other than `completed`, and an
operational failure only failed the job. The author saw nothing.

### Statuses and reasons

| Status | Reason (host code) | Cause |
| --- | --- | --- |
| `input_incomplete` | `required_input_omitted` | the diff didn't fit the request budget |
| `input_incomplete` | `all_changes_excluded_by_trusted_policy` | every changed file matches `review.exclude` |
| `invalid_output` | `invalid_output` | the model's reply failed validation |
| `review_failed` | `model_request_failed` | the model call returned an error (HTTP error, timeout, cap exceeded, refusal at transport level) |
| `review_failed` | `mirror_unavailable` | mirror sync failed or captured commits are missing |
| `review_failed` | `internal_error` | anything else after the change was captured |

`review_failed` is a comment status only; it is never stored as a review
result. The reason shown is always one of these codes. Raw error text,
`Result.Reasons` of an `invalid_output` run (which can quote model output),
HTTP bodies, endpoints and credentials never appear in a comment.

### Where it is published

- `reviewd comment --run <id>` and the service's `commentRun`: when the run's
  status is `input_incomplete` or `invalid_output`, the summary comment is a
  status comment instead of the review summary. `comment_mode`:
  - `post`: create/update the single owned summary comment (same
    `reviewd:summary` marker, same `CommentService`, same freshness and
    identity checks);
  - shadow (`""`): print it in `Body`, no remote calls;
  - `off`: nothing.
- Inline comments are not planned, posted, updated or marked outdated for
  these runs. Unless `inline_comment_mode` is `off` (which still reports
  `off`), the inline result is
  `{"State":"skipped","Reason":"review did not complete; inline comments left unchanged"}`,
  with no remote calls (not even a list).
- Approval is never attempted for them (`automateChange` skips `publishRun`).
- Exit code of `comment` is 0 when the status comment was published or
  printed; withheld keeps exit 4 as today.
- Service only, operational failures: when `forgeReview` fails after the
  change was captured and this is the job's final attempt
  (`j.Attempts >= serviceMaxAttempts`, where `const serviceMaxAttempts = 2`
  is also the worker's `RunOptions.MaxAttempts`), and `comment_mode` is
  `post`, a `review_failed` status comment is published for the attempt's run
  ID (the ID computed in `forgeReview`; no review is saved). The job still
  returns its original error. Earlier attempts post nothing. A failure before
  capture (forge API down) posts nothing.
- A later run that completes (or is partial) replaces the status comment with
  the normal summary through the usual update path.

### Wording (`publish.RenderStatus`)

```
<!-- reviewd:summary {change key} -->
<!-- reviewd:run {run ID} -->
### reviewd review (advisory): not completed

Commit: `{head, 12 chars}` · Status: `{status}` · Reason: `{reason}`

{what} {why}

What to do: {todo}

No approval was given. Inline comments from earlier reviews were left unchanged.
```

| Reason | what | why | todo |
| --- | --- | --- | --- |
| `required_input_omitted` | This change was not reviewed. | The diff is too large for the model's request budget, so nothing was sent to the model. | split the change, leave generated or vendored files out with `review.exclude`, set `review.large_changes: partial` to review as many files as fit, or use a model with a larger context window. |
| `all_changes_excluded_by_trusted_policy` | This change was not reviewed. | Every changed file matches `review.exclude`, so there was nothing to send to the model. | nothing, if that is intended. Otherwise narrow `review.exclude`. |
| `invalid_output` | The model's reply was rejected. | It did not pass validation (for example, a finding pointed outside the changed lines), so none of it is shown here. | {retry}. Operators can inspect the run with `reviewd explain --run {run ID}`. |
| `model_request_failed` | The review could not be completed. | The model request failed or timed out. | {retry}. Operators can find run `{run ID}` in the service log. |
| `mirror_unavailable` | The review could not be completed. | The local mirror could not provide the change's commits. | {retry}. Operators can find run `{run ID}` in the service log. |
| `internal_error` | The review could not be completed. | An internal error occurred. | {retry}. Operators can find run `{run ID}` in the service log. |

`{retry}` is ``comment `@{bot username} review` to try again, or push a new
commit`` when the bot username is known, else `push a new commit to try again`.
The username is validated (section 5); an invalid one is an error. Every value
in the template is a validated host fact (`RenderComment`'s header patterns
for key/run/head/status, the reason must be one of the six codes), so no line
starts with `/` and nothing model-derived is rendered.

Phrases tests rely on: `not completed`, `No approval was given`, `too large`,
`review.large_changes`, `review.exclude`, `rejected`,
`could not be completed`, `model request failed`, and the backticked
`` `@reviewd-bot review` `` hint.

## 2. Partial review of large changes

The specification (section on oversized PRs) keeps `input_incomplete` as the
default and allows "an explicit report-only partial mode [that] may send one
prompt, record every omitted hunk, and still prohibit approval". That is what
this adds, opt-in.

### Setting

`review.large_changes: incomplete | partial`, default `incomplete`.
Legacy forge config: `large_changes` (`""` means incomplete). It changes what
the model sees, so it is part of review identity (`reviewIdentity.LargeChanges`,
`omitempty`: configs without it keep their digest) and is listed in
`InvalidatesReviews` as `review.large_changes`.

### Packing (`review.Engine`)

`review.Input` gains `Files []FilePatch` (the kept diff's files in diff order:
`OldPath`, `NewPath`, `Patch`) and `Partial bool`. Forge reviews always pass
`Files`; the caller still passes the full `diff` item.

1. Pack as today. If the package is complete, nothing changes: the `diff`
   item is exactly the kept patch, the prompt is byte-identical to before.
2. If a required item other than `diff` was omitted, or `Partial` is false,
   the result is `input_incomplete` / `required_input_omitted`, zero model
   calls (as today).
3. Otherwise the `diff` item's content is rebuilt from whole files, **in diff
   order (path order, as git emits it), greedily**: each file is added if the
   whole package (all other items, required first) still fits; a file that
   doesn't fit is skipped and later files are still tried. A file whose own
   patch can't fit is therefore never reviewed. Files are never split.
4. If no file fits: `input_incomplete` / `required_input_omitted`.
5. Else one model call. Anchors are restricted to included files (old-side
   anchors of an included file's `OldPath`, new-side of its `NewPath`), so a
   finding or walkthrough entry on a file that wasn't sent is invalid output
   (`invalid_output`), like a finding on an excluded file.
6. A valid reply gives `Status: "partial"` (`reviewd review --url` exits 4,
   like any review that isn't `completed`), `Reasons: ["files_omitted_for_size"]`,
   `Package.Complete: false`, `EffectiveRisk = max(Minimum, model risk)`,
   `Eligible: false`. `Assess` keeps refusing anything that isn't
   `completed`, so a partial run can never become approvable.

Coverage, whenever `Files` is set: the usual item entries plus one
`file:<path>` entry per file (`NewPath`, or `OldPath` for a deletion) with
`included` or `not_included_budget`. Excluded files keep their
`excluded:<path>` entries. A complete run therefore lists every file as
`included`; this adds entries but changes no existing ID or status.

### Publication of a partial run

- Summary comment: the normal summary with `Status: partial`. `CommentContent`
  gains `NotReviewed int` (number of `not_included_budget` files; required
  `> 0` for status `partial`, must be 0 otherwise). The header gets, after the
  commit line:
  `Partial review: {n} changed file(s) were too large for the model's request budget and were not reviewed; they are listed in the report. Approval is withheld for partial reviews.`
  The paths are attacker-chosen, so they appear only inside the fenced
  report: `report.RenderReview` adds `Not reviewed (too large): {path}` per
  `not_included_budget` file entry.
- Inline comments: posted for findings (all of which are on included files).
  `publish.InlineRequest` gains `KeepAbsent bool`; partial runs set it, and
  then no owned comment is marked outdated (a file that wasn't reviewed says
  nothing about its old comments).
- Approval: `publishRun` returns `withheld`, exit 4, no forge writes, reason
  `partial review: some files were not reviewed; approval requires a complete review`.
  For any other non-`completed` status the reason is
  `review did not complete ({status})`. The service doesn't call `publishRun`
  for these runs at all.

### Request budget from the model config

`Cap: 1 << 20` is no longer hard-coded for live models.

`model.PromptBudget(c HTTPConfig) (int, error)` returns the largest prompt
(as `contextpack.Limits.MaxBytes`) such that any prompt produced by
`contextpack.Pack` of at most that size passes every cap `HTTP.Complete`
enforces before sending, with the default review schema:

```
attempts = Limits.MaxAttempts
tokens   = min(ContextTokens, MaxTotalTokens / attempts) - Limits.MaxOutputTokens
body     = min(MaxRequestBytes, tokens / InputTokensPerByte)
if PricingKnown and MaxCostMicros > 0 and InputPriceMicrosPerMillion > 0:
    micros = (MaxCostMicros / attempts) * 1_000_000 - Limits.MaxOutputTokens * OutputPriceMicrosPerMillion
    body   = min(body, micros / (InputPriceMicrosPerMillion * InputTokensPerByte))
overhead = len(request body Complete would send for an empty prompt)
budget   = (body - overhead) / 2
```

The `/ 2` covers re-escaping the prompt (JSON text whose `"` and `\` become
two bytes inside the request's JSON string; `Pack` output has no other
expanding bytes). Invalid configs and `budget <= 0` are errors. Arithmetic
must not overflow.

- `forgeReview` with `--model-config` (or a configured model) uses
  `Limits{MaxBytes: budget, MaxTokens: budget + 4096, ReserveTokens: 4096}`.
  An explicit `--max-bytes` lowers it further (`min`); its default no longer
  applies to live models.
- Saved `response:` fixtures keep `--max-bytes` / `1 << 20`.
- `automateChange` passes no cap; `forgeReview` derives it the same way.

### Identity impact

- `reviewIdentity` gains only `LargeChanges` (`omitempty`).
- The derived budget is not identity: it decides how much of the change
  fits, which is recorded per run in coverage and the package digest, like
  the cap is today.
- `forgeCapture`/provenance are unchanged.

## 3. Trigger controls (`review.auto`)

Automation settings: they decide whether the service starts a review, never
what a review sees, so they are **not** review identity and changing them
invalidates nothing.

```yaml
# changeharbor.yaml
version: 1
repositories:
  app:
    url: https://github.com/acme/app
    token_env: GITHUB_TOKEN
    mirror:
      path: /srv/mirrors/app.git
    policy: policies/app.json
    review:
      large_changes: partial
      auto:
        drafts: skip
        ignore_authors: ["dependabot[bot]", "renovate[bot]"]
        skip_labels: ["skip-review"]
        skip_title_keywords: ["[skip review]", "WIP"]
```

| Key | Default | Meaning |
| --- | --- | --- |
| `review.auto.drafts` | `skip` | `skip` or `review` draft PRs/MRs |
| `review.auto.ignore_authors` | none | author login/username patterns; `*` matches any run of characters, nothing else is special (`[bot]` is literal); case-insensitive |
| `review.auto.skip_labels` | none | label names, case-insensitive exact match |
| `review.auto.skip_title_keywords` | none | case-insensitive substrings of the title |

Legacy forge config: `auto_drafts` (`""` = skip), `auto_ignore_authors`,
`auto_skip_labels`, `auto_skip_title_keywords`.

Validation: `drafts` enum; each author pattern matches
`^[A-Za-z0-9*][A-Za-z0-9_.*\[\]-]*$` and is at most 255 bytes
(`repositories.<n>.review.auto.ignore_authors[i]: invalid author pattern`);
labels and keywords are non-empty (the generic empty-value error).

### Decision

`autoSkipReason(c forgeConfig, f changeFacts) string` returns the first of
`draft`, `ignored_author`, `skip_label`, `skip_title_keyword` that applies, or
`""`. `changeFacts{Draft bool; Author, Title string; Labels []string}` comes
from the forge API (the same pull/merge request read the review uses:
GitHub `draft`, `user.login`, `title`, `labels[].name`; GitLab `draft` or
`work_in_progress`, `author.username`, `title`, `labels`), never from the
webhook payload.

### Service behaviour

For a `review` event: withdraw stale approvals of that change (unchanged),
then read the facts; when skipped:

- no review, no model call, no comment, no approval;
- `skipped automatic review of {change key}: {reason}` on stderr (no title or
  author text);
- audit record: storage kind `review_skip`, key = change key
  (`gitlab:9:7`), value JSON `{"Reason":"draft","Head":"<API head>"}`.

`reviewd review --url` ignores `review.auto` (a deliberate human action).

### Events that start a review

GitHub `pull_request`: `opened`, `reopened`, `synchronize`,
`ready_for_review`, and `edited` when the payload has `changes.base`
(retarget). Merged `closed` stays ingest.

GitLab `Merge Request Hook` with `state: opened`:

- action `open`, `reopen`, or missing (older GitLab);
- action `update` only when the payload has `oldrev` (new commits),
  `changes.draft` or `changes.work_in_progress` with `current: false`
  (marked ready), or `changes.target_branch`.

Other GitLab MR actions (`approved`, `unapproved`, `approval`, `unapproval`,
label/title/description-only updates) no longer start a review. Before, every
event on an open MR did, including the bot's own approval. Removing a skip
label or title keyword takes effect on the next push or with the review
command. These payload fields only decide *whether* to enqueue; what is
reviewed and whether it's skipped come from the API.

## 4. Review command

A comment whose **first non-blank line** is exactly a command:

- `@{bot username} review` on GitHub and GitLab;
- `/changeharbor review` on GitHub only. GitLab treats line-leading `/` as
  quick actions, so it is not offered there.

Fields are whitespace-separated, case-insensitive, exactly two. Later lines
are ignored (room for a reason). Quotes (`> @bot review`), fences, extra
words on the line, or a mention elsewhere don't count. Usernames are
compared after removing one trailing `[bot]` from both sides, so a GitHub
App (`changeharbor[bot]`) is addressed as `@changeharbor` or
`@changeharbor[bot]`.

`parseReviewCommand(provider, body, botUsername string) bool`. With an empty
`botUsername`, only the GitHub `/changeharbor review` form can match.

### Normalisation

- GitHub `issue_comment`, action `created`, on a PR (`issue.pull_request`
  present): a `review` job, `ChangeID` `github:{repo}:{number}` (the PR's),
  generation = `comment.created_at` unix seconds.
- GitLab `Note Hook`, `noteable_type: MergeRequest`, action `create` or
  missing: a `review` job, `ChangeID` `gitlab:{project}:{merge_request.iid}`,
  generation = `object_attributes.created_at`.
- Only when the payload's body passes a cheap prefilter: first non-blank line
  has two fields, the second `review`, the first `@…` (or, GitHub only,
  `/changeharbor`). Anything else is not enqueued. The prefilter only saves
  jobs; the API copy is authoritative.
- Webhook authentication, delivery IDs and dedup are unchanged. A command
  shares its change's job key space, so a newer event for the change
  supersedes a pending command (and vice versa); both review the API head.

`worker.Event` gains `CommentID string`. For comment jobs `EventFromJob`
returns `Kind: "review"`, `RepositoryID`, `ChangeNumber` (`issue.number` /
`merge_request.iid`) and `CommentID` (`comment.id` / `object_attributes.id`);
`Head`, `Target` and `Branch` stay empty and that is allowed only for comment
jobs.

### Processing (`processEvent`, `CommentID != ""`)

The payload branch check is skipped (there is none); checks run in this
order, each a reason when it fails:

1. `no_forge_config`: the mapping has no forge configuration.
2. Read the comment from the API: GitHub
   `GET /repos/{owner}/{repo}/issues/comments/{id}`; GitLab
   `GET /projects/{id}/merge_requests/{iid}/notes/{note_id}`.
   `comment_mismatch`: GitHub comment's `issue_url` isn't this PR.
3. `system_note` (GitLab system note); `bot_author`: author ID is the bot
   user ID.
4. `bot_username_unknown`: no bot username and the body is an `@` command
   form; `not_a_command`: `parseReviewCommand` is false for the API body.
5. `unauthorized`: `commandAuthorized(provider, association, accessLevel)` is
   false. GitHub uses the API comment's `author_association` (`OWNER`,
   `MEMBER`, `COLLABORATOR` allowed, exact). GitLab reads
   `GET /projects/{id}/members/all/{author_id}` and needs
   `access_level >= 30` (Developer); 404 means not a member (level 0).
6. `branch_mismatch`: the API target branch differs from the configured one.

Every outcome is recorded: storage kind `review_command`, key = job ID, value
`{"Accepted":true,"Reason":""}` or `{"Accepted":false,"Reason":"<code>"}`.
Ignored commands are silent on the forge (no reply comment) and return nil
(no retry). An accepted command runs exactly the event path (withdraw stale
approval for that change, review the API head, comment, approve if
eligible) except that `review.auto` is bypassed, drafts included.

### Bot username

- `repositories.<n>.bot_username` (optional, validated
  `^[A-Za-z0-9][A-Za-z0-9_.-]*(\[bot\])?$`, at most 255 bytes) overrides.
- Otherwise `reviewd config resolve` stores it in the lock as `bot_username`:
  GitLab `/user` `username`; GitHub `/user` `login` when the token can call it
  (App installation tokens can't: set `bot_username: <app-slug>[bot]`).
- `LockInputs` gains `bot_username` and `LockedRepository` gains
  `bot_username`, both `omitempty`, so existing locks stay valid. A lock
  without it simply disables the `@` form until `config resolve` is rerun.
- Legacy forge config: `bot_username`. Not review identity.

## 5. `/health`

`"mode":"shadow"` was always reported. The payload is now:

```json
{"status":"ok","processing":"durable_worker",
 "repositories":{"gitlab:9":{"approvals":"submit","comments":"post","inline_comments":"shadow"}}}
```

- Keys are the service's `provider:repository_id` mapping keys; values use
  `config check`'s vocabulary (`forgeConfig.effective()`): approvals
  `off|shadow|submit`, comments and inline comments `off|shadow|post`.
- A mapping without forge configuration reports `off` for all three; one
  whose legacy forge file can't be loaded reports `unknown`.
- No `mode` key. No paths, URLs or environment variable names.
- `healthStatus(c serverConfig) healthReport`, with
  `healthReport{Status, Processing string; Repositories map[string]healthRepository}`
  and `healthRepository{Approvals, Comments, InlineComments string}`, JSON
  tags as above. The handler marshals it.

## 6. Config summary

| Key (changeharbor.yaml) | Legacy forge JSON | Default | Identity |
| --- | --- | --- | --- |
| `review.large_changes` | `large_changes` | `incomplete` | yes |
| `review.auto.drafts` | `auto_drafts` | `skip` | no |
| `review.auto.ignore_authors` | `auto_ignore_authors` | none | no |
| `review.auto.skip_labels` | `auto_skip_labels` | none | no |
| `review.auto.skip_title_keywords` | `auto_skip_title_keywords` | none | no |
| `bot_username` | `bot_username` | lock | no |

- `normalize` records sources `repositories.<n>.review.large_changes` and
  `repositories.<n>.review.auto.drafts` (`default`/`file`), creating `Review`
  and `Auto` when absent.
- Enum errors use the existing format, e.g.
  `repositories.app.review.large_changes: "split" is not one of incomplete, partial`,
  `repositories.app.review.auto.drafts: "never" is not one of skip, review`,
  `repositories.app.bot_username: invalid username`.
- `config check` `Effective` gains `large_changes`, `auto_drafts`,
  `auto_ignore_authors`, `auto_skip_labels`, `auto_skip_title_keywords`,
  `bot_username`; `Sources` gains `review.large_changes`,
  `review.auto.drafts` and `bot_username` (`file` or `lock`).
- `changeharbor.schema.json` documents every new key with a description;
  `website/docs/configuration/reference.md` documents each key path
  (`review.large_changes`, `review.auto`, `review.auto.drafts`, …,
  `bot_username`). Existing doc tests enforce both.
- `config migrate` carries the legacy fields over.

## 7. Security notes

- Status comments render host codes and validated host facts only.
- Skip decisions, command bodies, authors and permissions come from the API;
  payload fields only select which job to enqueue.
- The bot's own comments never trigger it (author ID, then system notes).
- Commands need write-level standing (GitHub association / GitLab Developer)
  and only cause a review that anyone with push access could cause by
  pushing; they can't skip any approval requirement.
- Partial runs can't be approved (`Status != "completed"`, incomplete
  package, `Assess` refuses), and findings outside the reviewed files are
  invalid.

## 8. Behaviour changes to existing features

- Drafts are skipped by default in the service.
- GitLab MR updates without new commits, readiness or retarget no longer
  start a review.
- `/health` payload (section 5).
- Inline comments of non-completed runs are no longer reconciled (before,
  an `invalid_output` run could mark every comment outdated).
- `publish --run` on a non-completed run returns `withheld` without forge
  calls.

No existing test is changed.

## API

- `cmd/reviewd`:
  - `forgeConfig` fields `LargeChanges` (`large_changes`), `AutoDrafts`
    (`auto_drafts`), `AutoIgnoreAuthors` (`auto_ignore_authors`),
    `AutoSkipLabels` (`auto_skip_labels`), `AutoSkipTitleKeywords`
    (`auto_skip_title_keywords`), `BotUsername` (`bot_username`), all
    `omitempty`; `reviewIdentity.LargeChanges` (`json:",omitempty"`);
  - `type changeFacts struct{ Draft bool; Author, Title string; Labels []string }`;
  - `func autoSkipReason(c forgeConfig, f changeFacts) string`;
  - `func parseReviewCommand(provider, body, botUsername string) bool`;
  - `func commandAuthorized(provider, association string, accessLevel int) bool`;
  - `func healthStatus(c serverConfig) healthReport`, `healthReport`,
    `healthRepository`;
  - `const serviceMaxAttempts = 2`.
- `internal/worker`: `Event.CommentID`; `normalize` rules above.
- `internal/review`: `type FilePatch struct{ OldPath, NewPath, Patch string }`;
  `Input.Files`, `Input.Partial`; status `partial`, reason
  `files_omitted_for_size`, coverage `file:<path>`.
- `internal/report`: `Not reviewed (too large): <path>` lines.
- `internal/model`: `func PromptBudget(c HTTPConfig) (int, error)`.
- `internal/publish`: `type StatusContent struct{ ChangeKey, RunID, Head, Status, Reason, BotUsername string }`;
  `func RenderStatus(StatusContent) (string, error)`;
  `CommentContent.NotReviewed int`; `InlineRequest.KeepAbsent bool`.
- `internal/config`: `ReviewSettings.LargeChanges` (`large_changes`),
  `ReviewSettings.Auto *AutoReviewSettings` (`auto`),
  `type AutoReviewSettings struct{ Drafts string; IgnoreAuthors, SkipLabels, SkipTitleKeywords []string }`
  (`drafts`, `ignore_authors`, `skip_labels`, `skip_title_keywords`),
  `RepositorySettings.BotUsername`, `LockInputs.BotUsername`,
  `LockedRepository.BotUsername` (`bot_username`), all `omitempty`.
- `internal/forge/github`: `Change` gains `Draft bool`, `Title`, `Author`,
  `TargetBranch string`, `Labels []string`;
  `type IssueComment struct{ ID, Body, AuthorID, AuthorLogin, AuthorAssociation string; IssueNumber int }`,
  `(*Client).IssueComment(ctx, owner, repo, id string) (IssueComment, error)`;
  `type User struct{ ID, Username string }`, `(*Client).Me(ctx) (User, error)`.
- `internal/forge/gitlab`: `Change` gains `Title`, `Author string`,
  `Labels []string` (and `Draft` also true for `work_in_progress`);
  `type Note struct{ ID, Body, AuthorID, AuthorUsername string; System bool }`,
  `(*Client).Note(ctx, projectID string, iid int, noteID string) (Note, error)`;
  `(*Client).AccessLevel(ctx, projectID, userID string) (int, error)` (404 is
  `0, nil`); `type User struct{ ID, Username string }`,
  `(*Client).Me(ctx) (User, error)`.
- Comment, note and user IDs passed to these clients are validated as
  positive decimal numbers before any request is made.

## Tests

End-to-end (CLI and `processEvent` against the fake GitLab/GitHub APIs, a
fake model endpoint, and the real webhook handler and queue):
`cmd/reviewd/status_e2e_test.go`, `partial_e2e_test.go`,
`triggers_e2e_test.go`, `commands_e2e_test.go`, `health_e2e_test.go`, with
shared fixtures in `feedback_fixtures_test.go`.

Unit: `cmd/reviewd/feedback_test.go`, `internal/worker/triggers_test.go`,
`internal/config/feedback_test.go`, `internal/review/partial_test.go`,
`internal/report/partial_test.go`, `internal/model/budget_test.go`,
`internal/publish/status_test.go`, `internal/forge/github/feedback_test.go`,
`internal/forge/gitlab/feedback_test.go`.

The existing `TestSchemaCoversEverySettingsKey` and
`TestDocsReferenceCoversEveryKey` also fail until the schema and
`website/docs/configuration/reference.md` document the new keys.
