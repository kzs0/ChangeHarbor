# Report renderer contract
Public API: `RenderRelease(release.Result) (ReleaseReports,error)`, where `ReleaseReports { JSON, Markdown, HTML, ChannelDraft string }`.
Renderer uses only validated host result and no model client. Deterministic stable output (no wall-clock calls). JSON marshals full result with null effective risk when absent. HTML uses html/template autoescaping. All human-readable outputs include immutable baseline/candidate, effective risk or explicit unknown, readiness, source-only limitation, outstanding unknowns/blockers/signoffs and known floors. Channel output explicitly marked draft and includes rollback caveat/request when unknown. Model-provided text cannot override host facts. No sending functionality exists. Review renderer is owned by review implementation unless agreed otherwise; avoid cycles.

## Extended renderers

`RenderReview(review.Result) (ReviewReports,error)` renders JSON/Markdown/HTML
including findings, evidence, host coverage/digest, model/floor/effective risk,
threshold and advisory eligibility with withholding reasons. HTML autoescapes all
source and model strings. It makes no model calls.

Release channel drafts now omit the long rationale and detailed interaction
sections while retaining all mandatory unknowns, blockers, signoffs, actions,
floors, source-only scope and rollout/rollback caveats. They include up to three
notable changes and a candidate/baseline keyed full-report artifact reference.
Classified item evidence and exact source anchors appear in the engineer report;
legacy string compatibility is explicitly labelled in reports.
