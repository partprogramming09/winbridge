//go:build windows

package files

import "errors"

func mkfifo(string) error { return errors.New("no FIFOs on Windows") }
