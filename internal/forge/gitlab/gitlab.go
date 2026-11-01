// Package gitlab records conservative capability availability for GitLab.
// SHA-bound transport is available, but verified protected CI provenance and
// complete native approval policy parity are not established; automatic submission
// remains unsupported until host diagnostics establish those requirements.
package gitlab

import "errors"

type Support struct{ RevisionBoundApproval, TrustedChecks bool }

func Capabilities() Support { return Support{} }
func RequireApprovalCapability() error {
	return errors.New("GitLab revision-bound approval is unsupported; remain in shadow mode")
}
