# Migrating from JSON configs

Earlier versions used one JSON forge file per repository (`--forge-config`)
and a separate service file. Both still work for one release, with a
deprecation warning. Convert them with:

```sh
reviewd config migrate \
  --forge-config /etc/reviewd/forge.json \
  --server-config /etc/reviewd/service.json \
  --state /var/lib/reviewd \
  --output changeharbor.yaml
```

- No network access is needed. The IDs in your JSON become
  `changeharbor.lock.yaml`.
- Reviews made with the old file stay publishable with the new one, because
  the review identity is unchanged.
- Old `mode: shadow` (no calls at all) becomes `publish.approvals: off`.
  Switch to `shadow` to see live *would approve* results.
- If your forge file mapped a fork, set `fork_source` before running
  `reviewd config resolve`. The command prints a warning.

Then run `reviewd config check` and drop `--forge-config` from your scripts.
