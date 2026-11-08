# Review guidance, path filters, suggestions and noise control

This extends inline review comments (`docs/design/inline-comments.md`, PR #10)
with the remaining core CodeRabbit capabilities that fit ChangeHarbor's
specification. Chat replies, learnings, tool execution, generated docstrings
or tests, and incremental (new-commits-only) reviews stay out of scope.

## 1. Trusted review instructions

The specification's prompt layout starts with "trusted task and guidance",
but no guidance layer existed. Repositories gain a `review` section:

```yaml
# changeharbor.yaml
version: 1
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: GITLAB_TOKEN
    mirror:
      path: /srv/mirrors/app.git
    policy: policies/app.json
    review:
      instructions: |
        Prefer explicit error handling. Flag any new global state.
      path_instructions:
        - paths: ["migrations/**"]
          instructions: Check that every migration can be rolled back.
        - paths: ["internal/auth/**", "cmd/**/auth*.go"]
          instructions: Treat authentication changes as security-sensitive.
      exclude: ["vendor/**", "**/*.pb.go", "go.sum"]
```

- **Trusted only.** Guidance comes from the reviewed config, never from the
  repository under review.
- **Prompt item.** The prompt gets one required item, ID and layer
  `trusted_guidance`. It contains the repository-wide `instructions` and only
  those `path_instructions` whose globs match a changed file (old or new
  path). Each entry is labelled with its patterns. With no guidance there is
  no item, so existing prompts don't change.
- **Globs** use `policy.Match` semantics (`*`, `?`, whole-segment `**`).
- **Size cap.** Total guidance (instructions plus every path instruction's
  text) is at most 16 KiB; more is a config error.
- **Legacy forge configs** accept `instructions`, `path_instructions` and
  `exclude`, all `omitempty`.

## 2. Path filters (`review.exclude`)

- **What changes.** The hunks of files matching `exclude` are removed from the
  diff sent to the model, and their anchors with them, so findings can't
  target an excluded file. A finding on one makes the review output invalid.
- **Coverage.** Each excluded file is recorded as
  `excluded:<path>` with status `excluded_by_trusted_policy` (the
  specification's term). Excluded files don't make coverage incomplete.
- **Report.** The summary report lists the excluded files as host text.
- **Risk policy is unaffected.** Floors and host policy facts are computed on
  the **full** diff, so excluding a file can't lower the risk score: a
  `path_rules` entry on an excluded path still raises the minimum.
- **Default:** nothing is excluded. A recommended starting list:
  - lockfiles: `go.sum`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`,
    `Cargo.lock`, `poetry.lock`;
  - vendored code: `vendor/**`, `third_party/**`, `node_modules/**`;
  - generated code: `**/*.pb.go`, `**/*_generated.go`, `**/zz_generated*.go`,
    `**/*.min.js`.

## 3. Review identity

Guidance and exclusions change the model's input, so they belong to the
review identity:

- `reviewIdentity` gains `Instructions`, `PathInstructions` and `Exclude`, all
  `omitempty`. A config without them keeps exactly its previous digest, so
  existing reviews stay publishable.
- `reviewd config check` lists `review.instructions`,
  `review.path_instructions` and `review.exclude` under `InvalidatesReviews`.

## 4. Committable suggestions

- **Model output.** A finding may carry an optional `suggestion` (replacement
  text) and an optional `suggestion_end_line` (>= `line`; default: `line`).
- **Validation.** A suggestion is only valid on the new side. Every line from
  `line` to `suggestion_end_line` must be a new-side changed line of the same
  path. `suggestion_end_line` without a `suggestion` is invalid, and the
  replacement is capped at 8 KiB. Any violation makes the review output
  invalid, like a bad anchor.
- **Rendering** appears after the finding's text fence, as a separate block:
  - GitHub: a `suggestion` fenced block. A multi-line suggestion is posted
    with `start_line`/`start_side` = the first line and `line` = the last.
  - GitLab: `suggestion:-0+N`, where `N = suggestion_end_line - line`,
    positioned at `line`.
- **Safety.**
  - The suggestion fence is longer than any backtick run in the replacement,
    so the replacement can't close it.
  - Control and bidi characters are stripped; tabs and newlines are kept.
  - On GitLab, a replacement line matching `^/[A-Za-z]` could be read as a
    quick action. The block is then dropped (the comment is still posted),
    with a host-written note saying why.
- **Setting:** `publish.suggestions: off | include`, default `include`. It only
  matters where inline comments are posted.
- **Fingerprints** don't include the suggestion, so changing a suggestion
  doesn't create a new comment.
- **Not autofix.** The specification excludes autofix and code writes. A
  suggestion writes nothing: a human reviews it and chooses to apply it.

## 5. Noise control

- **Setting:** `publish.inline_min_severity: info | low | medium | high |
  critical`, default `low`.
- Findings below the threshold aren't posted inline. They stay in the summary
  comment. `info` findings are therefore summary-only by default.

## 6. Still-reported findings are never marked outdated

PR #10's validation found that a finding still reported by the latest review,
but not posted inline, could have its earlier comment marked "possibly
outdated". Such findings include:

- findings below the severity threshold;
- findings GitLab can't place on its diff.

Now every fingerprint the latest review reports is passed to the publication
service as present (`InlineRequest.Present`). Only owned comments whose
fingerprint is absent from the review are marked outdated; still-reported
ones are kept with no write.

## API

- **`internal/publish`:**
  - `InlineOptions{Provider string; Suggestions bool; MinSeverity string}`.
  - `BuildInlineTargets(changeKey, runID, findings, opts)` returns the targets
    and the present-but-not-posted fingerprints.
  - `InlineContent` gains `Provider` and `Suggestions`.
  - `InlineTarget` gains `EndLine`.
- **`internal/forge/github`:** `NewReviewComment.StartLine`.
- **`internal/model`:** `Finding.Suggestion`, `Finding.SuggestionEndLine`.
- **`internal/review`:** `Input.Excluded` (files excluded by trusted policy).
- **`internal/config`:**
  - `RepositorySettings.Review *ReviewSettings{Instructions, PathInstructions,
    Exclude}`, with `PathInstruction{Paths, Instructions}`.
  - `PublishSettings.Suggestions` and `PublishSettings.InlineMinSeverity`.
- **`cmd/reviewd`:**
  - forge config fields `Instructions`, `PathInstructions []pathInstruction`,
    `Exclude`, `Suggestions`, `InlineMinSeverity`;
  - `guidanceItem(c, diff)`;
  - `excludeFiles(diff, patterns)`.
