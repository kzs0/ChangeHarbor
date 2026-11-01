// Package publish evaluates publication authority without performing writes.
package publish

import (
	"errors"
	"strings"
)

type Observation struct {
	Head, Target, PolicyDigest string
	ChecksPass                 bool
}
type Request struct {
	Mode, ReviewedHead, ReviewedTarget, PolicyDigest string
	Eligible                                         bool
}
type Decision struct {
	Approve bool
	Reason  string
}

func Gate(r Request, o Observation) (Decision, error) {
	if r.Mode == "" || r.Mode == "shadow" {
		return Decision{Reason: "shadow mode"}, nil
	}
	if r.Mode != "submit" {
		return Decision{}, errors.New("unsupported approval mode")
	}
	if !r.Eligible {
		return Decision{Reason: "ineligible review"}, nil
	}
	if r.ReviewedHead == "" || r.ReviewedTarget == "" || r.PolicyDigest == "" || r.ReviewedHead != o.Head || r.ReviewedTarget != o.Target || r.PolicyDigest != o.PolicyDigest {
		return Decision{Reason: "stale or missing revision/policy evidence"}, nil
	}
	if !o.ChecksPass {
		return Decision{Reason: "required checks not passed"}, nil
	}
	return Decision{Approve: true, Reason: "requirements passed; provider must bind approval to reviewed revision"}, nil
}

type Comment struct {
	ID, Body string
	Owned    bool
}

func OwnedComment(cs []Comment, marker string) (string, error) {
	if marker == "" {
		return "", errors.New("stable marker required")
	}
	id := ""
	for _, c := range cs {
		if c.Owned && strings.Contains(c.Body, marker) {
			if c.ID == "" || id != "" {
				return "", errors.New("ambiguous owned comment; reconcile before writing")
			}
			id = c.ID
		}
	}
	return id, nil
}
