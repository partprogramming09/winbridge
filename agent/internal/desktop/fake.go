package desktop

import (
	"fmt"
	"sync"
	"time"
)

// Fake is a Desktop that records what it was asked to do; it backs the tests of every layer above.
type Fake struct {
	mu          sync.Mutex
	Calls       []string
	Image       []byte
	WindowList  []Window
	ElementList []Element
	Fail        error
	Session     SessionState
	Held        []string
}

// record appends one call to the log, or returns the configured failure.
func (f *Fake) record(format string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	f.Calls = append(f.Calls, fmt.Sprintf(format, args...))
	return nil
}

// Log returns a copy of the recorded calls.
func (f *Fake) Log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Calls...)
}

func (f *Fake) Screenshot(o ScreenshotOptions) ([]byte, error) {
	if err := f.record("screenshot q=%d w=%d m=%d", o.Quality, o.MaxWidth, o.Monitor); err != nil {
		return nil, err
	}
	return f.Image, nil
}

func (f *Fake) Windows() ([]Window, error) {
	if err := f.record("windows"); err != nil {
		return nil, err
	}
	return f.WindowList, nil
}

func (f *Fake) Elements() ([]Element, error) {
	if err := f.record("elements"); err != nil {
		return nil, err
	}
	return f.ElementList, nil
}

func (f *Fake) SystemLanguage() string { return "en_US" }

func (f *Fake) Click(x, y int, button, action string) error {
	return f.record("click %d,%d %s %s", x, y, button, action)
}

func (f *Fake) MoveTo(x, y int, d time.Duration) error {
	return f.record("move %d,%d %s", x, y, d)
}

func (f *Fake) Drag(sx, sy int, hasStart bool, x, y int, d time.Duration) error {
	return f.record("drag from %d,%d(%t) to %d,%d %s", sx, sy, hasStart, x, y, d)
}

func (f *Fake) Scroll(amount int, horizontal bool) error {
	return f.record("scroll %d horizontal=%t", amount, horizontal)
}

func (f *Fake) TypeText(text string, mode TypeMode) error { return f.record("type %q %s", text, mode) }

func (f *Fake) Hotkey(keys []string) error { return f.record("hotkey %v", keys) }

func (f *Fake) PressKey(key string) error { return f.record("press %s", key) }

func (f *Fake) Focus(handle int64) (string, error) {
	if err := f.record("focus %d", handle); err != nil {
		return "", err
	}
	for _, w := range f.WindowList {
		if w.Handle == handle {
			return w.Title, nil
		}
	}
	return "", nil
}

func (f *Fake) SystemInfo() (string, error) {
	if err := f.record("sysinfo"); err != nil {
		return "", err
	}
	return "**System:** Fake", nil
}

// SessionState returns the state set by the test; the zero value is an unlocked Default desktop.
func (f *Fake) SessionState() SessionState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Session.Desktop == "" && !f.Session.Locked {
		return SessionState{Desktop: "Default"}
	}
	return f.Session
}

// HeldModifiers returns the modifiers set by the test.
func (f *Fake) HeldModifiers() []string { return f.Held }
