# Set up on GitLab

This guide takes one GitLab project from nothing to automatic, revision-bound
approvals. Each step can be checked, and nothing writes to GitLab until step 10.

!!! info "GitLab tier"
    By default ChangeHarbor only approves when the project's own protections
    are in place (step 4). Some of them (approval settings and rules) are
    Premium features. On GitLab Free, reviews and comments work, and approvals
    work only if you opt in to `publish.protections: advisory`.

## 1. Install ChangeHarbor

Follow [Install](../getting-started/install.md) on the host that will run it.

## 2. Create the bot

ChangeHarbor approves and comments as its own user. A **project access token**
creates one for you.

1. Go to **Settings → Access tokens → Add new token**.
2. Set **Role** to **Developer**. Developer can approve but can't merge;
   Maintainer works, but `doctor` warns that it's more than needed.
3. Select the **`api`** scope. `read_api` can't approve or comment.
4. Save the token where ChangeHarbor runs:

    ```sh
    export GITLAB_TOKEN=glpat-...
    ```

You don't need to look up the bot's user ID. `reviewd config resolve` finds it.

## 3. Make the pipeline definition trustworthy

The MR's pipeline proves your tests passed. But if the pipeline definition
lives in the MR's own `.gitlab-ci.yml`, the author can change what "passed"
means. Move it to a project MR authors can't edit:

1. Create a project such as `group/ci-config` and put the pipeline there,
   e.g. `app.yml`. Protect its default branch.
2. In the app project: **Settings → CI/CD → General pipelines → CI/CD
   configuration file** = `app.yml@group/ci-config`.
3. Make sure the jobs ChangeHarbor relies on run for merge requests:

    ```yaml
    test:
      script: make test
      rules:
        - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    ```

This is `gitlab.ci_trust: external_config`, the default. If you can't move the
file, see `unchanged_config` in the [reference](../configuration/reference.md#gitlab).

## 4. Configure merge requests

By default (`publish.protections: required`) ChangeHarbor approves only when
GitLab itself enforces these. In **Settings → Merge requests**:

| Setting | Value | Why |
| --- | --- | --- |
| Merge method | **Fast-forward merge** or **Merge commit with semi-linear history** (or enable **merged results pipelines**) | An MR can't merge on a stale target. |
| Merge checks → **Pipelines must succeed** | On | The pipeline gates the merge. |
| **Skipped pipelines are considered successful** | Off | A skipped pipeline proves nothing. |
| Approval settings → **When a commit is added** | **Remove all approvals** | An approval only ever covers the commit ChangeHarbor reviewed. |
| Approval rules | Include the bot (or use *All eligible users*) with 1 required approval | The bot's approval has to count. |

Then in **Settings → Repository → Protected branches**, protect `main` and
turn **Allowed to force push** off.

!!! warning "Approving without these protections"
    If you can't, or don't want to, set these, add
    `publish.protections: advisory` to the repository. ChangeHarbor then
    approves regardless of them, `doctor` reports them as warnings, and
    approvals work on GitLab Free. Either way, ChangeHarbor withdraws its own
    approval when the MR or its target moves.

## 5. Create a mirror

ChangeHarbor reads code only from a local mirror. A read-only **deploy token**
(**Settings → Repository → Deploy tokens**, scope `read_repository`) keeps
mirror credentials separate from the bot:

```sh
git clone --mirror \
  https://gitlab+deploy-token-123:TOKEN@gitlab.com/group/app.git \
  /srv/mirrors/app.git
```

With `mirror.sync: auto` (the default), ChangeHarbor fetches the target branch
and the MR's head from `origin` before every review and every approval check.

## 6. Write a policy and a model config

- [Risk policy](../configuration/policy.md): which paths and sizes need more
  care, and the highest risk you'll auto-approve.
- [Model config](../configuration/models.md): the endpoint and its limits.

## 7. Write `changeharbor.yaml`

```sh
reviewd config init --url https://gitlab.com/group/app \
  --mirror /srv/mirrors/app.git --policy policies/app.json --token-env GITLAB_TOKEN
```

Then add your model. The result looks like this:

```yaml
# changeharbor.yaml
version: 1
models:
  default: models/review.json
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: GITLAB_TOKEN
    mirror:
      path: /srv/mirrors/app.git
    policy: policies/app.json
    publish:
      approvals: shadow      # evaluate everything, write nothing
      comments: shadow
    gitlab:
      ci_trust: external_config
```

Commit `changeharbor.yaml` and `changeharbor.lock.yaml` with your
infrastructure code. `reviewd config check` shows every effective value and
where it came from.

## 8. Run the preflight

```sh
reviewd doctor
```

`doctor` checks the token, the bot's role, the CI configuration, the settings
from step 4, the approval rules and the mirror. It writes nothing. Fix anything
marked `error`: approvals are withheld until you do. With
`publish.protections: advisory`, the step 4 settings are reported as
`warning`s instead.

## 9. Try it in shadow mode

```sh
reviewd review --url https://gitlab.com/group/app/-/merge_requests/42
reviewd comment --run RUN_ID     # the summary and inline comments it would post
reviewd publish --run RUN_ID     # would approve / would withhold: reason
```

Do this on a few real MRs. Every *would withhold* reason is listed in
[Troubleshooting](../operations/troubleshooting.md).

## 10. Turn writes on

```yaml
# changeharbor.yaml
version: 1
models:
  default: models/review.json
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: GITLAB_TOKEN
    mirror:
      path: /srv/mirrors/app.git
    policy: policies/app.json
    publish:
      comments: post          # start with the summary comment...
      inline_comments: post   # ...and comments on the changed lines
      approvals: submit       # ...then approvals
```

## Steer and tune the review

Optional, and the same on GitHub:

```yaml
# changeharbor.yaml
version: 1
models:
  default: models/review.json
repositories:
  app:
    url: https://gitlab.com/group/app
    token_env: GITLAB_TOKEN
    mirror:
      path: /srv/mirrors/app.git
    policy: policies/app.json
    review:
      instructions: Prefer explicit error handling. Flag any new global state.
      path_instructions:
        - paths: ["migrations/**"]
          instructions: Check that every migration can be rolled back.
      exclude: ["vendor/**", "**/*.pb.go", "go.sum"]
    publish:
      inline_comments: post
      inline_min_severity: medium   # low (default) also posts low findings
      suggestions: include          # the default: one-click suggestion blocks
```

- **Instructions** are added to the review prompt as trusted guidance. Path
  instructions are added only when a changed file matches.
- **Excluded files** (a good start: lockfiles, `vendor/**`, generated code)
  never reach the model and can't get comments. They're listed in the summary,
  and risk-policy minimums still count them.
- **Suggestions** let reviewers apply a fix in one click. A suggestion
  containing a line that starts with `/` is left out on GitLab, where that
  line could run a quick action.
- **Large changes** that don't fit the model's request budget aren't
  reviewed: the summary comment says so and what to do. With
  `review.large_changes: partial`, the files that fit are reviewed and the rest
  are listed as not reviewed. Partial reviews are never approved.
- Changing `review` (except `review.auto`) invalidates existing reviews; the
  `publish` settings don't.

## 11. Automate with webhooks

Run the [service](service.md), then in **Settings → Webhooks → Add new
webhook**:

- **URL**: `https://changeharbor.example.com/webhooks/gitlab`
- **Secret token**: the value of `GITLAB_WEBHOOK_SECRET`
- **Triggers**: **Merge request events**, **Comments** (for the review
  command), and **Push events** filtered to `main`

From then on each MR that is opened, reopened, gets new commits, is marked
ready or is retargeted first has ChangeHarbor's approval of the previous head
withdrawn, then is reviewed, commented on and (when eligible) approved. Label,
title and description edits and approvals don't start a review. Drafts are
skipped until they are ready; `review.auto` can skip more. See
[Automatic reviews](service.md#automatic-reviews). Each push to `main`
re-checks earlier approvals and withdraws any whose target moved.

To review again, comment `@<bot_username> review` (for example
`@reviewd-bot review`) as the first line. The author needs Developer access or
above. The username is resolved into the lock by `reviewd config resolve`.
GitLab runs line-leading `/` commands as quick actions, so there is no slash
form.

## Fork merge requests

Fork pipelines run in the fork, which ChangeHarbor doesn't trust. To accept MRs
from one fork, set `fork_source: contributors/app`. A maintainer must then run
the MR pipeline in the parent project (**Run pipeline** on the MR) before
ChangeHarbor approves.
