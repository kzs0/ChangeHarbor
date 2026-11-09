# Rollout checklist

Turn writes on one kind at a time, and move on only when the previous step
looks right on real changes. Each step is a config change and a restart, and
each can be undone the same way. The examples use one repository, `api`;
the commands are the same on GitHub and GitLab.

## Before you start

- [ ] `reviewd` installed on the host, or the [container](container.md) ready.
- [ ] A bot identity: a [GitHub App](github.md#2-create-the-github-app) or a
      [GitLab project access token](gitlab.md#2-create-the-bot).
- [ ] A [model config](../configuration/providers.md) and a
      [risk policy](../configuration/policy.md).
- [ ] `changeharbor.yaml` with the repository and every `publish` mode left
      at its default, `shadow`.

```sh
reviewd config resolve      # writes changeharbor.lock.yaml
reviewd config check        # every effective value and where it came from
reviewd doctor              # GitLab only: token, role, CI trust, protections
```

## 1. Shadow, by hand

Review three to five recent changes, including a large one and one that
touches a path your policy cares about:

```sh
reviewd review --url https://github.com/acme/api/pull/7
reviewd comment --run RUN_ID     # the summary and inline comments it would post
reviewd publish --run RUN_ID     # would approve / would withhold: reason
reviewd explain --run RUN_ID --state STATE_DIR   # how the decision was reached
```

`STATE_DIR` is the config's `state`, by default `.changeharbor` next to
`changeharbor.yaml`. Check that:

- [ ] the findings are real and the walkthrough matches the change;
- [ ] generated and vendored files are in `review.exclude`, not in the
      findings;
- [ ] big changes either fit or get a clear "not reviewed" status (consider
      `review.large_changes: partial`, or a model with a bigger window);
- [ ] each *would withhold* reason makes sense (see
      [Troubleshooting](../operations/troubleshooting.md)).

Changing anything under `review` (other than `review.auto`) makes earlier
runs unpublishable, so review again after tuning.

## 2. Shadow, as a service

Start the [service](service.md) and add the webhook (GitHub App webhook, or
GitLab **Merge request events**, **Comments** and **Push events**). Nothing
is posted yet.

```sh
curl -s https://changeharbor.example.com/health
journalctl -u changeharbor -f        # or: docker compose logs -f reviewd
```

- [ ] `/health` lists the repository with every mode `shadow`.
- [ ] Opening a PR or pushing to one logs `saved forge review: RUN_ID`.
- [ ] Drafts and ignored authors log `skipped automatic review of ...`.
- [ ] Commenting `@<bot_username> review` on a draft logs a saved review.

While the service runs it holds the state lock, so `reviewd comment` and
`reviewd publish` need their own state directory to look at a change:

```sh
reviewd review --url https://github.com/acme/api/pull/9 --state /tmp/changeharbor-shadow
reviewd comment --run RUN_ID --state /tmp/changeharbor-shadow
```

## 3. Summary comments

```yaml
    publish:
      comments: post
```

Restart the service. Each reviewed change now gets one summary comment,
edited in place on later pushes.

- [ ] Summary comments appear within a few minutes of a push.
- [ ] Status comments (*not completed*) are rare, and their reasons are
      ones you understand.
- [ ] Authors know they can ask for another review with
      `@<bot_username> review`.

## 4. Inline comments

```yaml
    publish:
      comments: post
      inline_comments: post
      inline_min_severity: medium    # optional: start quieter
```

- [ ] Inline comments land on the right lines, and suggestions apply cleanly.
- [ ] The volume is acceptable. Raise `inline_min_severity` if it isn't;
      findings below it stay in the summary.

## 5. Approvals

First make the forge enforce what an approval relies on, or decide not to:

- GitHub: strict branch protection and mapped `github.required_checks`
  ([step 4](github.md#4-protect-the-branch)).
- GitLab: the merge request settings and a trusted pipeline, with
  `reviewd doctor` reporting no errors ([step 4](gitlab.md#4-configure-merge-requests)).
- Or set `publish.protections: advisory` and accept approvals without those
  guarantees.

Check by hand that low-risk changes would be approved and risky ones
wouldn't:

```sh
reviewd review --url https://github.com/acme/api/pull/12 --state /tmp/changeharbor-shadow
reviewd publish --run RUN_ID --state /tmp/changeharbor-shadow
```

Then:

```yaml
    publish:
      comments: post
      inline_comments: post
      approvals: submit
```

- [ ] Low-risk changes with passing checks get an approval of the reviewed
      commit.
- [ ] Pushing to an approved change withdraws the approval, then reviews the
      new commit.
- [ ] A push to the target branch withdraws approvals whose target moved.

## Backing out

Set the mode back to `shadow` (or `off`) and restart. Comments that were
posted stay, and so do approvals already given; remove those by hand if you
need them gone now.
