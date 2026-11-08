# Quickstart

Review one GitLab merge request in **shadow** mode. ChangeHarbor reads from
GitLab, but writes nothing. It takes about ten minutes. GitHub works the same
way: swap in a pull request URL and see the [GitHub guide](../guides/github.md)
for the token.

## 1. Get a token and a mirror

```sh
export GITLAB_TOKEN=glpat-...          # read access is enough for shadow mode
git clone --mirror https://gitlab.com/group/app.git /srv/mirrors/app.git
```

## 2. Write a minimal policy

`policies/app.json`:

<!-- example: policy -->
```json
{
  "base_minimum": 1,
  "auto_approve_max": 3
}
```

## 3. Create the config

```sh
reviewd config init \
  --url https://gitlab.com/group/app \
  --mirror /srv/mirrors/app.git \
  --policy policies/app.json \
  --token-env GITLAB_TOKEN
```

This writes three files next to each other:

- `changeharbor.yaml`: commented, with every write set to `shadow`.
- `changeharbor.schema.json`: gives your editor completion.
- `changeharbor.lock.yaml`: the IDs ChangeHarbor looked up.

To try it without a model, add a `response:` line pointing at the saved
response that ships in the repository:

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
    response: /path/to/ChangeHarbor/testdata/review-response.json
    publish:
      approvals: shadow
      comments: shadow
```

A saved response isn't a real review. Replace it with a
[model](../configuration/models.md) when you're ready.

## 4. Review a merge request

```sh
reviewd review --url https://gitlab.com/group/app/-/merge_requests/42
```

The review prints as JSON, and stderr shows `saved forge review: RUN_ID`.

## 5. See what it would do

```sh
reviewd comment --run RUN_ID    # prints the comment it would post
reviewd publish --run RUN_ID    # "would approve: …" or "would withhold: …"
```

Nothing was written to GitLab. Next, follow the [GitLab guide](../guides/gitlab.md)
to set up the project so approvals can actually be granted.
