package policy

import "fmt"

// UnknownOutcome identifies a dispatched operation whose effect cannot be
// confirmed. The cause is returned to the caller, never stored as raw journal
// content. Callers must inspect/reconcile, not automatically repeat the action.
func UnknownOutcome(id string, cause error) error {
	if cause == nil {
		cause = fmt.Errorf("dispatched effect could not be confirmed")
	}
	return fmt.Errorf("outcome_unknown: operation_id=%s: %w", id, cause)
}
