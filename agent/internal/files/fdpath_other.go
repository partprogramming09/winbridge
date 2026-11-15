//go:build !windows && !linux && !darwin

package files

import (
	"errors"
	"os"
)

func fdPath(*os.File) (string, error) { return "", errors.New("no descriptor path on this system") }
