# Git contract
Package git exports File {OldPath,NewPath,Patch string; Additions,Deletions int; Binary bool}, Diff {Base,Head,MergeBase string; Files []File; Patch string; TargetInHead bool}, and Reader{Repo string; MaxBytes int}. Methods Resolve(ctx, ref) (string,error), Diff(ctx,base,head) (Diff,error), Read(ctx,revision,path) ([]byte,error).
Resolve accepts refs but rejects option-looking/control-containing refs. Diff resolves immutable IDs, computes merge base, returns merge-base-to-head full diff with rename detection, counts unknown binary lines explicitly. Read pins revision, rejects absolute/traversal paths. Use bounded fixed Git argument arrays with external diff/textconv/hooks disabled. No repository code execution. Context cancellation and MaxBytes enforced; default max 8 MiB.

Implementation uses literal pathspecs, disabled replacement objects, no global or
system config, and no external diff/textconv/fsmonitor/hooks. Per-command output
is byte bounded; callers should set a deadline on the propagated context. Binary
and submodule line counts are -1 (unknown). No provider merge attribution is inferred.

Diff.Anchors contains canonical changed-line membership parsed from each pinned
file's Git hunks: Path, Line, Side (old/new). Context lines and binary/submodule
contents produce no anchors. Both deleted and added sides are retained.
