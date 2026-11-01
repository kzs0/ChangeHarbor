# GitLab capability contract
Capabilities() Support with fields RevisionBoundApproval,TrustedChecks bool. Initial conservative adapter may return false for unsupported capability; RequireApprovalCapability() error must reject unsupported revision-bound approval rather than pretending success. Live parity must be documented before enabling.
