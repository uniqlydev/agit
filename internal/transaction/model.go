package transaction

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Transaction struct {
	ID         string
	Objective  string
	State      State
	Branch     string
	HeadCommit string
	CreatedAt  time.Time
}

func ValidateObjective(objective string) error {
	if strings.TrimSpace(objective) == "" {
		return fmt.Errorf("transaction objective must not be empty")
	}
	if !utf8.ValidString(objective) || utf8.RuneCountInString(objective) > 4096 {
		return fmt.Errorf("transaction objective must be valid UTF-8 and at most 4096 characters")
	}
	return nil
}
