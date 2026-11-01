# Round 2 independent validation: review, policy and history

Read and inspected the added structured-output HTTP adapter, durable request
claims, exact changed-line anchors, strict structured budget profiles, monotone
policy layers and Git-backed journal/index/materialization.

Findings fixed during validation:
- HTTP reuse identity omitted provider endpoint. Added endpoint to the hashed
  identity, versioned the key and added two-endpoint regression coverage so an
  identically named model on another provider cannot reuse the wrong completion.
- Missing usage values were parsed as measured zero. Coordinated implementation
  fix to preserve usage as unknown and avoid reporting invented zero cost.
- History/index bounded Git writers embedded bytes.Buffer, promoting ReadFrom;
  io.Copy could bypass their Write caps. Coordinated named-buffer fix and public
  cap/unchanged-state regressions with history implementation owner.

Verified boundary design: no tools in outgoing model request; returned tool or
legacy function requests cannot become valid reviews; redirects disabled;
credential comes from named host environment and is excluded from persisted
request bodies. Request intent precedes HTTP; pending/ambiguous durable claims
refuse automatic replay. Only explicit 429/503 responses allow bounded identical
transport retries. Unknown price remains unknown; cost cap requires pricing;
request schema and serialized envelope enter token/byte reservations.

Canonical old/new changed-line anchors now validate inline findings in both Run
and inference-free Assess. Non-inline findings remain allowed. Policy layers use
maximum floors, minimum approval threshold and union of required named checks
and human signoffs. Config budget arithmetic subtracts reserves before summing
layers to avoid integer-overflow acceptance. History materialization selects only
ancestral snapshots, excludes stale/dependent claims and reports journal gaps.

Limitations: trusted identity/familiarity/check facts still require host/provider
collection; protocol success is not proof of semantic understanding; live native
protection and provenance enforcement must remain prerequisites for publication.
