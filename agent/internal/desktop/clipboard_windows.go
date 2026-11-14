//go:build windows

package desktop

import (
	"errors"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	pOpenClipboard              = user32.NewProc("OpenClipboard")
	pCloseClipboard             = user32.NewProc("CloseClipboard")
	pEmptyClipboard             = user32.NewProc("EmptyClipboard")
	pGetClipboardData           = user32.NewProc("GetClipboardData")
	pSetClipboardData           = user32.NewProc("SetClipboardData")
	pEnumClipboardFormats       = user32.NewProc("EnumClipboardFormats")
	pGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	pGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	pGlobalLock                 = kernel32.NewProc("GlobalLock")
	pGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	pGlobalSize                 = kernel32.NewProc("GlobalSize")
	pGlobalFree                 = kernel32.NewProc("GlobalFree")
)

const (
	cfText        = 1
	cfOEMText     = 7
	cfUnicodeText = 13
	cfLocale      = 16
	gmemMoveable  = 0x0002
	vkV           = 0x56
	vkControl     = 0x11

	// pasteSettle is how long the application gets to read the clipboard after Ctrl+V before the old text returns.
	pasteSettle = 800 * time.Millisecond
)

// openClipboard opens the clipboard, retrying briefly because another program may hold it for a moment.
func openClipboard() error {
	for i := 0; i < 20; i++ {
		if ok, _, _ := pOpenClipboard.Call(0); ok != 0 {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("the clipboard is busy")
}

func closeClipboard() { _, _, _ = pCloseClipboard.Call() }

// onlyTextOnClipboard reports whether every format on the open clipboard is a plain text format, so that restoring the
// text afterwards loses nothing.
func onlyTextOnClipboard() bool {
	format := uintptr(0)
	for {
		next, _, _ := pEnumClipboardFormats.Call(format)
		if next == 0 {
			return true
		}
		switch next {
		case cfText, cfOEMText, cfUnicodeText, cfLocale:
		default:
			return false
		}
		format = next
	}
}

// readClipboardText returns the Unicode text on the open clipboard; ok is false when there is none.
func readClipboardText() (text string, ok bool) {
	h, _, _ := pGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	size, _, _ := pGlobalSize.Call(h)
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 || size < 2 {
		return "", false
	}
	defer pGlobalUnlock.Call(h)
	units := make([]uint16, size/2)
	_, _, _ = pRtlMoveMemory.Call(uintptr(unsafe.Pointer(&units[0])), p, uintptr(len(units)*2))
	n := 0
	for n < len(units) && units[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(units[:n]), true
}

// setClipboardText replaces the content of the open clipboard with text.
func setClipboardText(text string) error {
	units := utf16.Encode([]rune(text))
	units = append(units, 0)
	h, _, _ := pGlobalAlloc.Call(gmemMoveable, uintptr(len(units)*2))
	if h == 0 {
		return errors.New("out of memory for the clipboard")
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		_, _, _ = pGlobalFree.Call(h)
		return errors.New("could not lock clipboard memory")
	}
	_, _, _ = pRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&units[0])), uintptr(len(units)*2))
	_, _, _ = pGlobalUnlock.Call(h)
	if ok, _, _ := pEmptyClipboard.Call(); ok == 0 {
		_, _, _ = pGlobalFree.Call(h)
		return errors.New("could not empty the clipboard")
	}
	if r, _, _ := pSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		_, _, _ = pGlobalFree.Call(h)
		return errors.New("could not set the clipboard")
	}
	return nil
}

// pasteText pastes text with Ctrl+V and puts the previous clipboard text back. It returns false, without touching
// anything, when the clipboard holds a non-text format that restoring would lose (or cannot be opened), so the caller
// can type the text instead.
func pasteText(text string) (bool, error) {
	if err := openClipboard(); err != nil {
		return false, nil
	}
	if !onlyTextOnClipboard() {
		closeClipboard()
		return false, nil
	}
	previous, hadText := readClipboardText()
	if err := setClipboardText(text); err != nil {
		closeClipboard()
		return false, err
	}
	closeClipboard()
	ours, _, _ := pGetClipboardSequenceNumber.Call()

	layout := foregroundLayout()
	ctrl, _, _ := pMapVirtualKeyExW.Call(vkControl, 0, layout)
	v, _, _ := pMapVirtualKeyExW.Call(vkV, 0, layout)
	if err := sendInputs(
		keyInput(vkControl, uint16(ctrl), 0), keyInput(vkV, uint16(v), 0),
		keyInput(vkV, uint16(v), keyUp), keyInput(vkControl, uint16(ctrl), keyUp),
	); err != nil {
		return false, err
	}
	time.Sleep(pasteSettle)

	// Restore only if nobody changed the clipboard meanwhile.
	if now, _, _ := pGetClipboardSequenceNumber.Call(); now == ours {
		if err := openClipboard(); err == nil {
			if hadText {
				_ = setClipboardText(previous)
			} else {
				_, _, _ = pEmptyClipboard.Call()
			}
			closeClipboard()
		}
	}
	return true, nil
}
