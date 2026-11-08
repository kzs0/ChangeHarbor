# Set up on GitHub

This guide sets up one GitHub repository for reviews, summary comments and
revision-bound approvals.

!!! note "Approvals don't depend on branch protection"
    ChangeHarbor approves whenever `publish.approvals` is `submit` and its own
    evidence holds: trusted checks, risk policy, unchanged commits. When the
    PR moves, it dismisses its own review. `reviewd doctor` is GitLab-only
    today.

## 1. Install ChangeHarbor

Follow [Install](../getting-started/install.md).

## 2. Create the bot identity

Use either a **machine user** with a fine-grained personal access token, or a
**GitHub App**.

=== "Machine user"

    Create a fine-grained token for the machine user, scoped to the
    repository, with:

    - **Pull requests**: Read and write (comments and approvals)
    - **Contents**: Read
    - **Checks** and **Commit statuses**: Read

    ```sh
    export GITHUB_TOKEN=github_pat_...
    ```

    `reviewd config resolve` reads the user ID from `/user`.

=== "GitHub App"

    Grant the same permissions. Installation tokens can't call `/user`, so set
    the app's bot user ID yourself:

    ```sh
    gh api users/YOUR-APP-NAME%5Bbot%5D --jq .id
    ```

    Put that number in `bot_user_id`. Minting and rotating installation tokens
    into `GITHUB_TOKEN` is up to your deployment.

## 3. Protect the branch (recommended)

ChangeHarbor approves either way. Branch protection decides what GitHub itself
enforces at merge time. In **Settings → Branches** (or a ruleset) for `main`:

- **Require a pull request before merging** with **1** required approval, so
  ChangeHarbor's approval (or a human's) gates the merge.
- **Dismiss stale pull request approvals when new commits are pushed**, so
  everyone's approvals follow the same rule ChangeHarbor applies to its own.
- **Require status checks to pass**, with **Require branches to be up to date**.

## 4. Map required checks to their producers

A check's *name* proves nothing on its own, so ChangeHarbor also checks *which
app produced it*. Find the app ID for each required check:

```sh
gh api repos/OWNER/REPO/commits/SHA/check-runs \
  --jq '.check_runs[] | {name, app: .app.id}'
```

GitHub Actions is app `15368`.

## 5. Create a mirror

```sh
git clone --mirror https://x-access-token:TOKEN@github.com/OWNER/REPO.git \
  /srv/mirrors/repo.git
```

With `mirror.sync: auto`, ChangeHarbor fetches `main` and `refs/pull/N/head`
before each review and approval check.

## 6. Write `changeharbor.yaml`

```yaml
# changeharbor.yaml
version: 1
models:
  default: models/review.json
repositories:
  api:
    url: https://github.com/OWNER/REPO
    token_env: GITHUB_TOKEN
    # bot_user_id: "41898282"   # GitHub Apps only
    mirror:
      path: /srv/mirrors/repo.git
    policy: policies/api.json
    publish:
      approvals: shadow
      comments: shadow
    github:
      required_checks:
        test: "15368"      # check name: producing app ID
        lint: "15368"
```

Then:

```sh
reviewd config resolve     # writes changeharbor.lock.yaml
reviewd config check
```

For GitHub Enterprise Server, set `provider: github`. The API defaults to
`https://HOST/api/v3`; override it with `api_url`.

## 7. Try it in shadow mode

```sh
reviewd review --url https://github.com/OWNER/REPO/pull/7
reviewd comment --run RUN_ID
reviewd publish --run RUN_ID
```

## 8. Turn writes on and automate

Set `publish.comments: post`, then `publish.approvals: submit`. Run the
[service](service.md), then add a webhook in **Settings → Webhooks**:

- **Payload URL**: `https://changeharbor.example.com/webhooks/github`
- **Content type**: `application/json`
- **Secret**: the value of `GITHUB_WEBHOOK_SECRET`
- **Events**: **Pull requests** and **Pushes**
