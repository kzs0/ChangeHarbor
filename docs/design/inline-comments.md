# Inline review comments

ChangeHarbor posts one summary comment per change (see `publish.comments`).
This design adds CodeRabbit-style **inline comments**: one comment per
finding, pinned to the changed line it is about, plus a per-file
**walkthrough** in the summary. There are no chat replies (the spec allows
one model call per review and no conversation) and no suggested-fix blocks
yet (a follow-up).

## Configuration

`publish.inline_comments: off | shadow | post`, default `shadow`, beside
`publish.comments` in `changeharbor.yaml`. It is a publication setting, so it
is excluded from review identity and changing it never invalidates reviews.
The legacy forge config field is `inline_comment_mode` (omitted = render
only, `off`, `post`); `post` requires `bot_user_id`.

| Mode | Behaviour |
| --- | --- |
| `off` | Nothing. No remote calls. |
| `shadow` | Renders the comments ChangeHarbor would post (the plan) and prints them. No remote calls. |
| `post` | Creates, keeps, marks or unmarks inline comments as below. |

`reviewd comment --run` handles the summary and the inline comments, each per
its own mode, and prints both results (the summary fields at the top level,
inline results under `Inline`). `reviewd serve` runs it after a review when
either `comments` or `inline_comments` is `post`.

## Model output: walkthrough

The review response gains an optional `walkthrough`:
`[{"path": "...", "summary": "..."}]`. Paths must be safe relative paths,
unique, with non-empty summaries, and every path must be a file the diff
changed (a path in the canonical changed-line anchors), with the same
strictness as finding anchors: anything else makes the review output invalid.
The walkthrough is rendered in the summary report, inside the summary
comment's existing code fence.

## Finding identity

A finding's **fingerprint** is the first 16 hex characters of the SHA-256 of
its path, category (trimmed, lowercased) and title (lowercased, whitespace
collapsed), separated unambiguously. It doesn't include the line, so a
finding that moves when code shifts keeps its identity, and a re-run after a
push doesn't duplicate it. Findings with the same fingerprint in one review
collapse to one comment. Findings without a valid anchor stay in the summary
only.

## Comment body

Each inline comment carries, outside its code fence:

- `<!-- reviewd:inline <changeKey> <fingerprint> -->`
- `<!-- reviewd:run <runID> -->`
- severity and category, only when they are safe tokens.

Model text (title, trigger, consequence) is inside a code fence longer than
any backtick run it contains, with control and bidi characters removed and
line-leading `/` indented, exactly like the summary comment, so it can't
trigger GitLab quick actions, mentions or markup. Bodies are capped below
GitHub's comment limit.

A comment marked outdated additionally carries `<!-- reviewd:outdated -->`
and the visible line "Possibly outdated: not reported by the latest review."
Marking and unmarking are idempotent and reversible.

**Ownership:** a comment is ChangeHarbor's only if the bot wrote it **and**
it carries the inline marker for this change. Comments by anyone else are
never edited, whatever markers they contain.

## Reconciliation

Given the review's anchored findings and the owned inline comments on the
change, ChangeHarbor plans (a pure function, tested without a network):

| Situation | Action |
| --- | --- |
| Finding has no owned comment | **Create** it. |
| Owned comment exists, not outdated | **Keep** (no write). |
| Owned comment is outdated, finding is back | **Unmark** (edit). |
| Owned comment's finding is gone | **Mark outdated** (edit). Never delete or resolve. Already-outdated comments are left alone. |
| Several owned comments with one fingerprint | Keep one; create nothing. |

## Publication

Like the summary comment:

- Withheld, with no writes, when the reviewed head or target moved, or the
  token isn't the bot (GitLab verifies up front, GitHub from the write).
- Intent is audited `pending` (kind `inline_publication`, id = run) before any
  write. A published run is final: re-running it writes nothing.
- A retry after an ambiguous outcome re-lists the comments; because planning
  dedups by fingerprint, nothing is duplicated.
- Definite refusals (401/403/404/422) are audited `withheld` and retryable.
  5xx and transport errors stay pending.
- Every write's author must be the bot.

## Forge calls

**GitHub**

- List: `GET /repos/{o}/{r}/pulls/{n}/comments` (paginated).
- Create: one review, `POST /repos/{o}/{r}/pulls/{n}/reviews` with
  `event: COMMENT`, `commit_id` = reviewed head, and
  `comments: [{path, line, side: RIGHT|LEFT, body}]`.
- Edit: `PATCH /repos/{o}/{r}/pulls/comments/{id}`.

**GitLab**

- List: `GET /projects/:id/merge_requests/:iid/discussions` (paginated;
  first note of each discussion; system notes skipped).
- Create: `POST /projects/:id/merge_requests/:iid/discussions` with `body` and
  `position {position_type: text, base_sha, start_sha, head_sha, old_path,
  new_path, new_line | old_line}`, from the MR's `diff_refs` (`Change` now
  exposes `StartOID`).
- Edit: `PUT /projects/:id/merge_requests/:iid/notes/:note_id`.
