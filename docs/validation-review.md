# Independent review validation

Reviewed Git, context packing, model protocol, and review orchestration separately
from their test and implementation authors.

Fixed and regression-tested:
- Duplicate JSON object keys at any nesting level now invalidate model output,
  preventing ambiguous last-value-wins risk parsing.
- Finding paths reject absolute paths, traversal, empty/dot segments, backslashes
  and control characters rather than accepting unsafe anchor references.

Existing tests exercise exact one-completion review, invalid-output no repair,
required overflow zero inference, optional omissions, evidence identity validation,
immutable local Git reads, option-looking revisions, and byte caps. Git commands
use fixed argument arrays, disabled external diff/textconv and fsmonitor/hooks,
no replace objects and literal paths. Packing accounts serialized UTF-8 bytes as
a conservative token bound, including reserve; no byte/4 hard-limit claim.

Remaining limitation: finding path/line membership is not checked against a
canonical changed-hunk manifest. Current eligibility is advisory and CLI keeps
independent requirements false; live submission must not be enabled until this
manifest validation and trusted provider/native protection integration exists.
No model HTTP adapter or provider usage/cost enforcement is delivered here.

Verification: `go test -race` and `go vet` pass for all four owned packages
(`internal/git`, `internal/contextpack`, `internal/model`, `internal/review`).
