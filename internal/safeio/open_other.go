//go:build !darwin && !linux

package safeio

import (
	"fmt"
	"os"
)

func OpenFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	return nil, fmt.Errorf("AGIT M1 requires macOS or Linux local filesystems")
}
