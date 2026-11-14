//go:build !windows

package procs

import "errors"

// List is not available outside Windows.
func List() ([]Process, error) {
	return nil, errors.New("listing processes is only supported on Windows")
}
