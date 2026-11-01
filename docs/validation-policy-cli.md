# Independent policy and CLI validation

Reviewed strict configuration and policy loading, maximum floor evaluation,
rename matching, unknown history/size behavior, and independent requirements.
The focused configuration/policy suites pass. No threshold can bypass stale
revisions, failed checks, missing evidence, or shadow mode.

CLI integration now accepts a trusted flat `review --policy FILE` JSON policy.
It derives paths and line metrics from the complete Git manifest before packing,
uses both rename paths, marks binary/submodule counts unknown, and treats all
area history as unknown. Floors combine by maximum; command-line thresholds
combine conservatively with policy thresholds. Matched rule IDs and explicit
human-required state are retained in result reasons. Independent requirements
remain false; the CLI cannot submit approval.

Added regression tests for sensitive renames with binary unknown counts, and a
real temporary Git repository review smoke test that checks completed output,
model risk 2, and shadow-only ineligibility. CLI commands now run within real
Bedrock review.run/release.run operations while retaining listener-free setup.

Limitations: CLI policy uses the documented flat internal schema, not the full
v3 wrapper. No verified historical familiarity backfill is wired into CLI.
Saved release input is explicit caller-provided fixture evidence rather than
live Git/forge verification. Invalid command input is not yet consistently
classified into the spec's exit-code-2 convention across all commands.
