# Risk policy

The policy sets the **minimum** risk a change can have, from facts the model
can't influence, and the **highest** risk ChangeHarbor may approve. It's a
strict JSON file referenced by `policy` in `changeharbor.yaml`.

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
