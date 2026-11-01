# Local review contract
Input {Items []contextpack.Item; Limits contextpack.Limits; EvidenceIDs []string; Minimum,Threshold int; RequirementsPass bool}. Result {Status string; Package contextpack.Package; Review model.Review; Minimum,EffectiveRisk,Threshold int; Eligible bool; Reasons []string}. Engine {Model model.Model}. Run(ctx,Input)(Result,error). Assess(Result, minimum,threshold int,requirementsPass bool)(Result,error).
Run packs deterministically; required overflow yields input_incomplete, zero calls, no effective risk, no eligibility. One valid response yields completed and maximum model/minimum risk. Invalid response yields invalid_output with zero effective risk/no eligibility, no second call. Transport failure returns error. Validate configured scores in 1..10. Eligibility is a local advisory/shadow computation, NOT submission authority: threshold inclusive, complete valid result and all independent requirements. Assess uses stored validated result, no Model access, same arithmetic; refuses invalid/incomplete result. Evidence IDs must refer to input items (must not allow a caller to invent references absent prompt). No publishing. CLI must label local eligibility advisory and never submit approval.


Input.Anchors carries canonical git.Diff.Anchors. Inline findings require exact
path/line/side membership; omitted Side defaults to new. Non-inline findings use
empty path and line 0. Result persists anchors and reassessment revalidates them.
Caller must derive anchors from pinned Git, never from model output.
