package lifecycle

import (
	"github.com/kksty/tianwu/internal/collab"
	"github.com/kksty/tianwu/internal/model"
)

// uncountAttempt reverses the claim counter for a run we ended ourselves. A
// timeout, a manual cancel or a lost dispatcher is our decision, not the
// executor's failure, so it must not move the direction up the ladder (D19).
func uncountAttempt(i *model.Intent) {
	if i.Attempts > 0 {
		i.Attempts--
	}
}

// participantID is the mechanical participant identity of one direction. It is
// the same formula the collaboration package issues; kept here so the failure
// path does not import the API.
func participantID(intentID string) string { return collab.ParticipantID(intentID) }
