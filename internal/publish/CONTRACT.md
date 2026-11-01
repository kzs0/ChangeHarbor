# Publication gate contract
Observation fields Head,Target,PolicyDigest string; ChecksPass bool. Request fields Mode,ReviewedHead,ReviewedTarget,PolicyDigest string; Eligible bool.
Gate(Request,Observation) (Decision,error), Decision.Approve bool and Reason string. shadow default and empty mode never approve. submit requires eligible exact head/target/policy and passed checks. Unsupported modes invalid.
OwnedComment(comments []Comment, marker string) (string,error), Comment fields ID,Body string; Owned bool. Select only owned stable marker comment, never human; multiple owned marker matches error to force reconciliation. Returns empty ID for create. No network calls in policy helpers.
