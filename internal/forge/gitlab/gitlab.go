// Package gitlab contains bounded GitLab REST v4 adapters and the native
// requirement gate used before a revision-bound approval is submitted.
//
// Submission is supported only when live platform facts establish every native
// control the host relies on: approvals reset on push, a protected target that
// rejects force pushes, pipelines required to succeed with natively enforced
// target freshness, a trusted head pipeline bound to the reviewed revision, and
// approval rules that the configured bot can satisfy. Anything unknown —
// including Premium-only endpoints that are unavailable — withholds approval.
package gitlab
