# Set up on GitHub

This guide takes one GitHub repository from nothing to reviews, summary and
inline comments, the review command and, last, revision-bound approvals.
Nothing writes to GitHub until step 8.

ChangeHarbor can act as a **GitHub App** or as a **machine user** with a
fine-grained token. Use the App unless you have a reason not to: it doesn't
take a seat, its tokens last an hour and are scoped to one repository, and
one App covers every repository you install it on.

!!! note "Branch protection"
    By default ChangeHarbor approves only when the target branch has strict
    branch protection (step 4). It can't prove its approval satisfies a
    *required approving reviews* rule, so it withholds when one is set. To
    approve regardless of branch protection, set
    `publish.protections: advisory`. When the PR moves, ChangeHarbor
    dismisses its own review in either mode. `reviewd doctor` checks GitLab
    only, so on GitHub the shadow runs in step 7 are your preflight.

## 1. Install ChangeHarbor

Follow [Install](../getting-started/install.md), or use the
[container image](container.md).

## 2. Create the GitHub App

In **Settings → Developer settings → GitHub Apps → New GitHub App** (under
your organization's settings for an org-owned App):

1. Give it a name. Its slug becomes the bot's login, `<slug>[bot]`.
2. **Webhook**: active, URL `https://changeharbor.example.com/webhooks/github`,
   and a random secret. Keep the secret; it becomes `GITHUB_WEBHOOK_SECRET`.
3. **Repository permissions**:

    | Permission | Access | Used for |
    | --- | --- | --- |
    | Pull requests | Read and write | Reading PRs, the summary comment, inline comments, approving and dismissing its review |
    | Contents | Read | Cloning and fetching the mirror |
    | Checks | Read | Reading check runs on the reviewed commit |
    | Issues | Read | Receiving *Issue comment* events and reading the review command |
    | Administration | Read | Reading branch protection; not needed with `publish.protections: advisory` |
    | Metadata | Read | Required by GitHub |

4. **Subscribe to events**: **Pull request**, **Push** and **Issue comment**.
5. Create it, note the **App ID**, and generate a private key.
6. **Install App** on the repositories ChangeHarbor should review.

Where ChangeHarbor runs, put the PEM in an environment variable, with its
line breaks:

```sh
export CHANGEHARBOR_APP_KEY="$(cat review-app.private-key.pem)"
```

ChangeHarbor signs a short-lived JWT with the key, exchanges it for an
installation token scoped to the one repository, and refreshes the token
five minutes before it expires. Tokens, JWTs and the key stay in memory.

??? info "Using a machine user instead"
    Create a GitHub user for the bot, add it to the repository as a
    collaborator with **Write** access, and create a fine-grained token for
    it, scoped to the repository, with the permissions in the table above
    (Issues isn't needed). Then:

    ```sh
    export GITHUB_TOKEN=github_pat_...
    ```

    In step 6, use `token_env: GITHUB_TOKEN` in place of `github.app`, and
    set `mirror.auth: token` so git clones with the token too. Add
    the webhook in the repository's **Settings → Webhooks** with the same
    URL and secret, content type `application/json`, and the events
    **Pull requests**, **Pushes** and **Issue comments**.

## 3. Write a policy and a model config

- [Risk policy](../configuration/policy.md): which paths and sizes need more
  care, and the highest risk you'll approve.
- [Model config](../configuration/models.md) from one of the
  [provider recipes](../configuration/providers.md).

## 4. Protect the branch

With the default `publish.protections: required`, set these in
**Settings → Branches** for `main` (a classic branch protection rule):

- **Require status checks to pass before merging**, with **Require branches
  to be up to date before merging** (strict).
- Select the checks that must pass. Each must also be mapped in
  `github.required_checks` (next step).
- Leave **Require approvals** off. See the note above.

With `publish.protections: advisory`, ChangeHarbor approves whatever these
are.

## 5. Map required checks to their producers

A check's name proves nothing on its own, so ChangeHarbor also checks which
app produced it. Find the app ID for each required check:

```sh
gh api repos/OWNER/REPO/commits/SHA/check-runs \
  --jq '.check_runs[] | {name, app: .app.id}'
```

GitHub Actions is app `15368`.

## 6. Write `changeharbor.yaml`

```yaml
# changeharbor.yaml
version: 1
models:
  default: models/review.json
repositories:
  api:
    url: https://github.com/OWNER/REPO
    github:
      app:
        app_id: 123456
        private_key_env: CHANGEHARBOR_APP_KEY
        # installation_id: 7890123   # optional; config resolve looks it up
      required_checks:
        test: "15368"      # check name: producing app ID
        lint: "15368"
    mirror:
      path: /srv/mirrors/repo.git   # cloned on first use
    policy: policies/api.json
    publish:
      approvals: shadow
      comments: shadow
```

The mirror is cloned on the first review and fetched before every review and
approval check. With an App, git authenticates with the installation token,
passed to git only through its environment (`mirror.auth: token`, the
default for an App). Nothing is stored in the mirror's remote URL.

Then:

```sh
reviewd config resolve     # writes changeharbor.lock.yaml
reviewd config check
```

`config resolve` checks that the key belongs to `app_id`, looks up the
installation, and records it in the lock with the App's bot user and
username (`<slug>[bot]`). Commit `changeharbor.yaml` and the lock.

For GitHub Enterprise Server, set `provider: github`. The API defaults to
`https://HOST/api/v3`; override it with `api_url`.

## 7. Try it in shadow mode

```sh
reviewd review --url https://github.com/OWNER/REPO/pull/7
reviewd comment --run RUN_ID     # prints the summary and inline comments
reviewd publish --run RUN_ID     # would approve / would withhold: reason
```

Do this on a few real pull requests. Every *would withhold* reason is in
[Troubleshooting](../operations/troubleshooting.md).

## 8. Turn writes on and automate

Run the [service](service.md) (or the [container](container.md)) and turn
writes on in stages, following the [rollout checklist](rollout.md):
`publish.comments: post`, then `publish.inline_comments: post`, then
`publish.approvals: submit`.

The service reviews a pull request when it's opened, reopened, gets new
commits, is marked ready for review or is retargeted. Drafts are skipped
until they're ready, and `review.auto` can skip more (bot authors, labels,
title keywords). See [Automatic reviews](service.md#automatic-reviews).
Each push to `main` re-checks earlier approvals and dismisses any whose
target moved.

To review again, comment `@<slug> review` (or `@<slug>[bot] review`) or
`/changeharbor review`, on the first line. The comment's author must be the
repository's owner, a member of the organization, or a collaborator.

With a machine user, the `@` form uses the user's login, which
`config resolve` records. If you set `bot_user_id` yourself, set
`bot_username` too.

## Steer and tune the review

`review.instructions`, `review.path_instructions` and `review.exclude` add
trusted guidance and leave generated or vendored files out of the review.
`publish.inline_min_severity` controls how chatty inline comments are, and
`publish.suggestions` turns suggestion blocks on or off. See
[Steer and tune the review](gitlab.md#steer-and-tune-the-review); it works
the same on GitHub.
