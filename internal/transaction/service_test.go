package transaction

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type failingStore struct{ calls int }

func (s *failingStore) Create(context.Context, Transaction) error {
	s.calls++
	return errors.New("storage failure")
}
func TestObjectiveValidation(t *testing.T) {
	for _, objective := range []string{"", " \n\t", strings.Repeat("a", 4097), string([]byte{0xff})} {
		store := &failingStore{}
		if _, err := (Service{Store: store}).Begin(context.Background(), objective, "main", ""); err == nil {
			t.Fatalf("accepted %q", objective)
		}
		if store.calls != 0 {
			t.Fatal("persisted invalid objective")
		}
	}
	store := &failingStore{}
	if _, err := (Service{Store: store}).Begin(context.Background(), "valid", "main", ""); err == nil || err.Error() != "storage failure" {
		t.Fatalf("%v", err)
	}
}
func TestStateTransitions(t *testing.T) {
	states := []State{StateActive, StateCompleted, StateAborted, "unknown"}
	for _, from := range states {
		for _, to := range states {
			expected := from == StateActive && (to == StateCompleted || to == StateAborted)
			if valid := ValidateTransition(from, to) == nil; valid != expected {
				t.Fatalf("%s -> %s: %v", from, to, valid)
			}
		}
	}
}
