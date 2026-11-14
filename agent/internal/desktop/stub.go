//go:build !windows

package desktop

import "time"

// New returns a desktop that reports ErrUnsupported: this build is not for Windows.
func New() (Desktop, error) { return unsupported{}, nil }

type unsupported struct{}

func (unsupported) Screenshot(ScreenshotOptions) ([]byte, error) { return nil, ErrUnsupported }
func (unsupported) Windows() ([]Window, error)                   { return nil, ErrUnsupported }
func (unsupported) Elements() ([]Element, error)                 { return nil, ErrUnsupported }
func (unsupported) SystemLanguage() string                       { return "en_US" }
func (unsupported) Click(int, int, string, string) error         { return ErrUnsupported }
func (unsupported) MoveTo(int, int, time.Duration) error         { return ErrUnsupported }
func (unsupported) Drag(int, int, bool, int, int, time.Duration) error {
	return ErrUnsupported
}
func (unsupported) Scroll(int, bool) error          { return ErrUnsupported }
func (unsupported) TypeText(string, TypeMode) error { return ErrUnsupported }
func (unsupported) Hotkey([]string) error           { return ErrUnsupported }
func (unsupported) PressKey(string) error           { return ErrUnsupported }
func (unsupported) Focus(int64) (string, error)     { return "", ErrUnsupported }
func (unsupported) SystemInfo() (string, error)     { return "", ErrUnsupported }
func (unsupported) SessionState() SessionState      { return SessionState{Desktop: "unsupported"} }
func (unsupported) HeldModifiers() []string         { return nil }
