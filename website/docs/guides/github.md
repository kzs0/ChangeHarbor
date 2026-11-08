# Set up on GitHub

This guide sets up one GitHub repository for reviews, summary and inline comments and
revision-bound approvals.

!!! note "Branch protection"
    By default ChangeHarbor approves only when `main` has strict branch
    protection (step 3). Because it can't prove its approval satisfies a
    *required approving reviews* rule, it withholds when one is set. To approve
    regardless of branch protection, opt in with `publish.protections:
    advisory`. When the PR moves, ChangeHarbor dismisses its own review in
    either mode. `reviewd doctor` is GitLab-only today.

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
    - **Administration**: Read (to read branch protection; not needed with
      `publish.protections: advisory`)

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

## 3. Protect the branch

With the default `publish.protections: required`, set in **Settings →
Branches → Branch protection rules** for `main`:

- **Require status checks to pass**: on, with **Require branches to be up to
  date** on (strict).
- Select the checks that must pass. Each must also be mapped in
  `github.required_checks` (step 4).
- Don't set **required approving reviews**. See the note above.

With `publish.protections: advisory`, ChangeHarbor approves whatever these are.

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

Set `publish.comments: post` and `publish.inline_comments: post` (one comment per
finding on its changed line, posted as a single review), then
`publish.approvals: submit`. Run the
[service](service.md), then add a webhook in **Settings → Webhooks**:

- **Payload URL**: `https://changeharbor.example.com/webhooks/github`
- **Content type**: `application/json`
- **Secret**: the value of `GITHUB_WEBHOOK_SECRET`
- **Events**: **Pull requests** and **Pushes**

## Steer and tune the review

`review.instructions`, `review.path_instructions` and `review.exclude` add
trusted guidance and leave generated or vendored files out of the review.
`publish.inline_min_severity` controls how chatty inline comments are, and
`publish.suggestions` adds one-click suggestion blocks (multi-line suggestions
span the changed lines they replace). See
[Steer and tune the review](gitlab.md#steer-and-tune-the-review) for an example; it
works the same on GitHub.
