package transaction

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Store is the persistence boundary used by transaction creation.
type Store interface {
	Create(context.Context, Transaction) error
}
type Service struct{ Store Store }

func (s Service) Begin(ctx context.Context, objective, branch, head string) (Transaction, error) {
	objective = strings.TrimSpace(objective)
	if err := ValidateObjective(objective); err != nil {
		return Transaction{}, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Transaction{}, fmt.Errorf("generate transaction ID: %w", err)
	}
	tx := Transaction{ID: hex.EncodeToString(id[:]), Objective: objective, State: StateActive, Branch: branch, HeadCommit: head, CreatedAt: time.Now().UTC()}
	if err := s.Store.Create(ctx, tx); err != nil {
		return Transaction{}, err
	}
	return tx, nil
}
