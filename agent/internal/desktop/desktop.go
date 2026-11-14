// Package desktop is the part of the agent that touches the Windows desktop. The tools only see the Desktop
// interface, so everything above it is tested on any system with a fake.
package desktop

import (
	"errors"
	"time"
)

// ErrUnsupported is returned by the desktop of a system that is not Windows.
var ErrUnsupported = errors.New("the desktop tools run only on Windows")

// Window is a visible top-level window with a title.
type Window struct {
	Handle                   int64
	Title                    string
	Left, Top, Right, Bottom int
}

// Width returns the window width in pixels.
func (w Window) Width() int { return w.Right - w.Left }

// Height returns the window height in pixels.
func (w Window) Height() int { return w.Bottom - w.Top }

// Element is a visible child control of the foreground window.
type Element struct {
	Index                    int
	Class, Text              string
	Left, Top, Right, Bottom int
}

// ScreenshotOptions mirrors the Snapshot parameters.
type ScreenshotOptions struct {
	Quality  int // JPEG quality 1 to 100
	MaxWidth int // 0 keeps the native size
	Monitor  int // 0 is every monitor, 1 and up one monitor
	Region   *Region
}

// Region is a screen rectangle in virtual-desktop pixels.
type Region struct{ X, Y, Width, Height int }

// SessionState says whether the interactive desktop can take input.
type SessionState struct {
	Locked  bool   // the lock screen or a secure desktop (UAC) is in front
	Desktop string // name of the input desktop: Default, Winlogon, or "secure" when it cannot be opened

	// ElevatedForeground is the title of the foreground window when it belongs to a process with more privileges than
	// the agent: Windows then drops the agent's mouse and keyboard input. Empty when there is none.
	ElevatedForeground string
}

// TypeMode says how Type delivers text.
type TypeMode string

const (
	// TypePaste puts the text on the clipboard and sends Ctrl+V, then restores the clipboard text. It is exact for any
	// character and fast; the agent falls back to TypeKeys when the clipboard holds something other than text.
	TypePaste TypeMode = "paste"
	// TypeKeys sends one Unicode input event per character, paced. Use it where pasting is blocked.
	TypeKeys TypeMode = "keys"
)

// Desktop is everything the tools need from the machine.
type Desktop interface {
	Screenshot(ScreenshotOptions) ([]byte, error)
	Windows() ([]Window, error)
	Elements() ([]Element, error)
	SystemLanguage() string
	Click(x, y int, button, action string) error
	MoveTo(x, y int, duration time.Duration) error
	Drag(startX, startY int, hasStart bool, x, y int, duration time.Duration) error
	Scroll(amount int, horizontal bool) error
	TypeText(text string, mode TypeMode) error
	Hotkey(keys []string) error
	PressKey(key string) error
	Focus(handle int64) (title string, err error)
	SystemInfo() (string, error)
	SessionState() SessionState
	// HeldModifiers names the modifier keys Windows believes are held down right now (shift, ctrl, alt, win).
	HeldModifiers() []string
}
