# Set up on GitLab

This guide takes one GitLab project from nothing to automatic, revision-bound
approvals. Each step can be checked, and nothing writes to GitLab until step 10.

!!! info "Works on every GitLab tier"
    Reviews, comments and approvals all work on GitLab Free, Premium and
    Ultimate. ChangeHarbor approves whenever `publish.approvals` is `submit`
    and its own evidence holds. It doesn't depend on your merge settings.

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

## 4. Choose merge settings (recommended, not required)

ChangeHarbor approves regardless of these settings. They decide what GitLab
itself enforces at merge time, so they're good practice:

| Setting (**Settings → Merge requests**) | Recommended | Without it |
| --- | --- | --- |
| Merge method | **Fast-forward** or **semi-linear**, or **merged results pipelines** | An MR can merge onto a newer target than the one reviewed. ChangeHarbor withdraws its approval when the target moves. |
| **Pipelines must succeed** | On | GitLab lets a failing MR merge. ChangeHarbor still only approves after its required jobs pass. |
| **Skipped pipelines are considered successful** | Off | Skipped pipelines count for merging, though never for ChangeHarbor. |
| Approval settings → **When a commit is added** | **Remove all approvals** | Other people's approvals survive new commits. ChangeHarbor withdraws *its own* approval whenever the MR changes. |
| Approval rules | Make the bot an eligible approver | The bot's approval doesn't count toward that rule. |

In **Settings → Repository → Protected branches**, protecting `main` with
**Allowed to force push** off keeps history from changing under reviewed code.

`reviewd doctor` reports each of these as a warning when it's off.

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
marked `error`: ChangeHarbor can't work until you do. `warning`s describe
protection GitLab itself isn't providing.

## 9. Try it in shadow mode

```sh
reviewd review --url https://gitlab.com/group/app/-/merge_requests/42
reviewd comment --run RUN_ID     # the comment it would post
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
      comments: post       # start with comments...
      approvals: submit    # ...then approvals
```

## 11. Automate with webhooks

Run the [service](service.md), then in **Settings → Webhooks → Add new
webhook**:

- **URL**: `https://changeharbor.example.com/webhooks/gitlab`
- **Secret token**: the value of `GITLAB_WEBHOOK_SECRET`
- **Triggers**: **Merge request events**, and **Push events** filtered to
  `main`

From then on each MR update first withdraws ChangeHarbor's approval of the
previous head, then reviews, comments and (when eligible) approves the new
one. Each push to `main` re-checks earlier approvals and withdraws any whose
target moved.

## Fork merge requests

Fork pipelines run in the fork, which ChangeHarbor doesn't trust. To accept MRs
from one fork, set `fork_source: contributors/app`. A maintainer must then run
the MR pipeline in the parent project (**Run pipeline** on the MR) before
ChangeHarbor approves.
