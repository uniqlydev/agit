package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

type State string

const (
	Creating State = "creating"
	Ready    State = "ready"
	Error    State = "error"
	Removing State = "removing"
	Removed  State = "removed"
)

type Workspace struct {
	ID, TransactionID, Name, Branch, Path, BaseCommit string
	State                                             State
	CreatedAt, UpdatedAt                              time.Time
	OperationToken, Operation, AdminPath, LastError   string
	RemovedAt                                         time.Time
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func ValidateName(name string) error {
	if !namePattern.MatchString(name) || name[len(name)-1] == '-' {
		return fmt.Errorf("workspace name must be 1-48 lowercase ASCII letters/digits/hyphens, start with a letter, and not end in a hyphen")
	}
	return nil
}
func ValidID(id string) bool { return idPattern.MatchString(id) }
func NewID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}
func ValidateTransition(from, to State) error {
	allowed := map[State][]State{
		Creating: {Ready, Error}, Ready: {Error, Removing},
		Error: {Ready, Removing, Removed}, Removing: {Error, Removed},
	}
	for _, state := range allowed[from] {
		if to == state {
			return nil
		}
	}
	return fmt.Errorf("invalid workspace transition %s -> %s", from, to)
}
