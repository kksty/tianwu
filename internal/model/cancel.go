package model

// CancelHoldsIntent distinguishes an explicit stop from a continuation or
// process-level interruption. Settlement and crash recovery share this rule.
func CancelHoldsIntent(cause string) bool {
	switch cause {
	case CancelCauseOperator, CancelCauseStopLoss, CancelCauseWorkerKill, CancelCauseModelStall:
		return true
	default:
		return false
	}
}
