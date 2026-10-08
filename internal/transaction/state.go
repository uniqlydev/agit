package transaction

import "fmt"

type State string

const (
	StateActive    State = "active"
	StateCompleted State = "completed"
	StateAborted   State = "aborted"
)

func (s State) Validate() error {
	switch s {
	case StateActive, StateCompleted, StateAborted:
		return nil
	}
	return fmt.Errorf("invalid transaction state %q", s)
}
func ValidateTransition(from, to State) error {
	if err := from.Validate(); err != nil {
		return err
	}
	if err := to.Validate(); err != nil {
		return err
	}
	if from == StateActive && (to == StateCompleted || to == StateAborted) {
		return nil
	}
	return fmt.Errorf("invalid transaction transition %s -> %s", from, to)
}
