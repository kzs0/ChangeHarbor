# Risk policy

The policy sets the **minimum** risk a change can have, from facts the model
can't influence, and the **highest** risk ChangeHarbor may approve. Set it
with `policy` in `changeharbor.yaml`: a path to a strict JSON file, a YAML
file (the name ends in `.yaml` or `.yml`), or the fields inline. See
[Where the policy lives](#where-the-policy-lives).

```text
effective risk = max(model_merge_risk, highest matching minimum)
approvable     = effective risk <= auto_approve_max  (and every other requirement holds)
```

Scores run from 1 (lowest risk) to 10 (highest).

## A complete example

<!-- example: policy -->
```json
{
  "base_minimum": 1,
  "auto_approve_max": 3,
  "required_checks": ["test"],
  "path_rules": [
    {
      "id": "auth",
      "paths": ["internal/auth/**"],
      "minimum": 7,
      "explanation": "Authentication changes always need a human."
    },
    { "id": "migrations", "paths": ["migrations/**"], "minimum": 5 }
  ],
  "size_rules": [
    { "id": "large", "metric": "changed_lines", "at_least": 400, "minimum": 5 },
    { "id": "wide", "metric": "touched_files", "at_least": 30, "minimum": 5 }
  ],
  "unknown_size": { "action": "minimum", "minimum": 5 },
  "familiarity_metric": "prior_authored_merged_integrations",
  "lookback_days": 180,
  "areas": [{ "id": "core", "paths": ["internal/**"] }],
  "tiers": [
    { "at_most_prior_merges": 0, "minimum": 5 },
    { "at_most_prior_merges": 3, "minimum": 3 }
  ],
  "otherwise_minimum": 1,
  "unknown_familiarity": { "action": "minimum", "minimum": 4 }
}
```

## Where the policy lives

A file whose name ends in `.yaml` or `.yml` is read as YAML with the same
strict rules as `changeharbor.yaml` (no anchors, duplicate keys or nulls);
any other file is JSON. The same policy in YAML:

```yaml
# policies/app.yaml
base_minimum: 1
auto_approve_max: 3
required_checks: [test]
path_rules:
  - id: auth
    paths: ["internal/auth/**"]
    minimum: 7
    explanation: Authentication changes always need a human.
unknown_size:
  action: minimum
  minimum: 5
```

Or put the fields directly under `policy`. An inline policy in `defaults`
applies to every repository that doesn't set its own; a repository's policy
replaces it as a whole.

```yaml
# changeharbor.yaml
version: 1
repositories:
  app:
    url: https://gitlab.com/group/app
    auth:
      token_env: GITLAB_TOKEN
    mirror:
      path: /srv/mirrors/app.git
    policy:
      base_minimum: 1
      auto_approve_max: 3
      required_checks: [test]
      path_rules:
        - id: auth
          paths: ["internal/auth/**"]
          minimum: 7
```

Inline policies are checked when the file is loaded, and errors name the
config path: `repositories.app.policy.path_rules[0].minimun: unknown key; did
you mean "minimum"?`. Policy files are read when a command uses them, and
errors start with the file's path.

The policy digest depends only on the rules, so a JSON file, a YAML file and
an inline mapping with the same fields have the same digest.

## Fields

| Field | Meaning |
| --- | --- |
| `base_minimum` | Minimum for every change. |
| `auto_approve_max` | Highest effective risk that may be approved. Changing only this doesn't invalidate existing reviews. |
| `required_checks` | Check or job names that must pass. On GitLab these are also the default `gitlab.required_jobs`. |
| `path_rules` | Minimum when any changed path (old or new name) matches a glob. `explanation` is shown to reviewers. |
| `size_rules` | Minimum when `changed_lines` or `touched_files` is at least `at_least`. |
| `unknown_size` | What to do when size can't be measured (binary files): `{"action": "minimum", "minimum": N}` or `{"action": "human_required", "minimum": 0}`. |
| `familiarity_metric`, `lookback_days` | Count the author's prior merged changes in each area over the lookback window. |
| `areas` | Named path groups for familiarity. |
| `tiers` | Minimum by prior merges, ascending `at_most_prior_merges`, with non-increasing minimums. |
| `otherwise_minimum` | Minimum for authors past every tier. |
| `unknown_familiarity` | What to do when history is incomplete, e.g. on a new installation. |

## Globs

`*` matches within one path segment, `?` one character, and `**` whole
segments (zero or more). Absolute paths, `..`, brackets, braces and negation
are rejected.

## Changing the policy

Changing anything except `auto_approve_max` changes the policy digest, so
existing reviews can't be published; review the change again. The threshold
alone is re-evaluated live at publish time without a new model call.
