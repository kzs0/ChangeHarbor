package publish

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// BotUsernamePattern is the form a configured or resolved bot username must
// have before it is rendered or compared.
var BotUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*(\[bot\])?$`)

// ValidBotUsername reports whether u is a usable bot username.
func ValidBotUsername(u string) bool { return len(u) <= 255 && BotUsernamePattern.MatchString(u) }

// StatusContent holds the host facts of a review that did not complete.
type StatusContent struct {
	ChangeKey, RunID, Head, Status, Reason, BotUsername string
}

type statusWording struct{ what, why, todo string }

const (
	notReviewed  = "This change was not reviewed."
	notCompleted = "The review could not be completed."
	retryMarker  = "{retry}"
)

var statusReasons = map[string]statusWording{
	"required_input_omitted": {notReviewed,
		"The diff is too large for the model's request budget, so nothing was sent to the model.",
		"split the change, leave generated or vendored files out with `review.exclude`, set `review.large_changes: partial` to review as many files as fit, or use a model with a larger context window."},
	"all_changes_excluded_by_trusted_policy": {notReviewed,
		"Every changed file matches `review.exclude`, so there was nothing to send to the model.",
		"nothing, if that is intended. Otherwise narrow `review.exclude`."},
	"invalid_output": {"The model's reply was rejected.",
		"It did not pass validation (for example, a finding pointed outside the changed lines), so none of it is shown here.",
		retryMarker + ". Operators can inspect the run with `reviewd explain --run {run}`."},
	"model_request_failed": {notCompleted, "The model request failed or timed out.",
		retryMarker + ". Operators can find run `{run}` in the service log."},
	"mirror_unavailable": {notCompleted, "The local mirror could not provide the change's commits.",
		retryMarker + ". Operators can find run `{run}` in the service log."},
	"internal_error": {notCompleted, "An internal error occurred.",
		retryMarker + ". Operators can find run `{run}` in the service log."},
}

// StatusReasonKnown reports whether reason is one of the status comment
// reason codes.
func StatusReasonKnown(reason string) bool { _, ok := statusReasons[reason]; return ok }

// RenderStatus builds the summary comment for a review that did not
// complete. Every value is a validated host fact or fixed text; nothing
// model-derived or taken from an error is rendered.
func RenderStatus(c StatusContent) (string, error) {
	if !changeKeyPattern.MatchString(c.ChangeKey) || !tokenPattern.MatchString(c.RunID) || !hexPattern.MatchString(c.Head) || !statusPattern.MatchString(c.Status) {
		return "", errors.New("invalid status comment facts")
	}
	w, ok := statusReasons[c.Reason]
	if !ok {
		return "", errors.New("unknown status comment reason")
	}
	if c.BotUsername != "" && !ValidBotUsername(c.BotUsername) {
		return "", errors.New("invalid bot username")
	}
	retry := "push a new commit to try again"
	if c.BotUsername != "" {
		retry = "comment `@" + c.BotUsername + " review` to try again, or push a new commit"
	}
	todo := strings.ReplaceAll(w.todo, "{run}", c.RunID)
	if strings.HasPrefix(todo, retryMarker) {
		todo = strings.Replace(todo, retryMarker, retry, 1)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n### reviewd review (advisory): not completed\n\n", SummaryMarker(c.ChangeKey), RunMarker(c.RunID))
	fmt.Fprintf(&b, "Commit: `%s` · Status: `%s` · Reason: `%s`\n\n", c.Head[:min(12, len(c.Head))], c.Status, c.Reason)
	fmt.Fprintf(&b, "%s %s\n\nWhat to do: %s\n\n", w.what, w.why, todo)
	b.WriteString("No approval was given. Inline comments from earlier reviews were left unchanged.\n")
	return b.String(), nil
}
