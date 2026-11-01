# Independent release, reporting, archive and evaluation validation

## Release and reports

Validated the original vertical slice and then the expanded classified-output and operational-evidence implementation. New classified output validates per-item classification, exact evidence IDs and host-whitelisted source anchors. Operational documents verify candidate/build identity, trusted producer allowlist, timestamp freshness/expiry, strict fields and bounded input. Regression tests cover future timestamps, exact expiry, wrong build candidate, duplicate producers, and byte overflow. The producer allowlist is a host configuration boundary: a local JSON document is not a cryptographically authenticated external attestation.

Fixed defects with independent regression tests:

- Duplicate JSON keys and missing/null required output lists were accepted. Output now rejects these ambiguous or incomplete responses.
- A stored non-enum risk could retain completed status and ready readiness with nil effective risk. Re-evaluation now marks it insufficient/incomplete.
- Invalid classified model claims were assigned to result.Items before validation and could appear in an incomplete report. Items now become renderable only after every item validates.
- The prompt lacked explicit output field names and enums; it now includes the structured response contract.
- Channel rollout/recovery notes lost item classification. They now preserve factual/suggested/unknown labels, and absent rollout details have an explicit engineer request.

Standalone HTML uses html/template and escapes hostile source/model text. Review reports retain host coverage, floors, thresholds, withholding reasons, findings and evidence. Concise channel drafts omit long rationale while retaining unknowns, blockers, signoffs, floors and rollback caveats. Rendering performs no inference or outbound delivery.

Compatibility string items remain opt-in through the historical false RequireClassifiedItems setting; new composition must set this flag true. Host scope facts are not established by arbitrary release.Input JSON: source ancestry/journal and operational-document validation belong in composition. Historical reports retain their captured evidence; reusing them for a new assessment requires renewed freshness validation.

## Archive and offline evaluation

Validated real SQLite/CAS round-trip plus traversal rejection. Fixed restore accepting an empty state.db, which SQLite regards as a valid fresh database: restore now requires an existing SQLite header, supported schema version and records table before publication. Added regression proving failed restores never publish. Backup now rejects dangling destination symlinks and atomically publishes with a no-replace hard link, preventing a concurrent destination from being overwritten.

Evaluation rejects duplicate fixture keys, including case variants, so a later duplicate cannot silently override a failing expected outcome. Offline protocol replay is distinct from semantic model-quality evaluation.

## Checks

Focused standard tests pass for release, report, archive and evaluate. Focused race tests and vet are run separately; see final validation record for their results. These tests require no model credentials or external network.

## Final selector and classified-schema review

Release path selectors were checked against full old/new rename manifests. Regression tests confirm all matching floors are in the serialized prompt before inference and that signoff/risk reevaluation performs no additional completion. Factual classified items can cite the CLI's net_diff and release_inventory identities and exact host-whitelisted net-diff source anchors.

Fixed required-plan semantics: verified_absent no longer satisfies a selector's RequiredEvidence. A verified absence of a recovery plan must trigger the configured missing-plan blocker, while verified_absent remains available for ordinary completeness categories. Also made iteration over classified model groups deterministic so identical responses produce consistently ordered caveats.

Final focused race tests and vet pass for release, report, archive and evaluate using the isolated final cache. No external HTTP or model service was used by these tests.
