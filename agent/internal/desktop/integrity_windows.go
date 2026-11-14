//go:build windows

package desktop

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// elevatedForegroundTitle returns the title of the foreground window when its process has a higher integrity level than
// the agent, and "" when it does not or when that cannot be told: not knowing must never block input that would work.
func elevatedForegroundTitle() string {
	hwnd, _, _ := pGetForegroundWindow.Call()
	if hwnd == 0 {
		return ""
	}
	var pid uint32
	pGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ""
	}
	own, ok := tokenIntegrity(windows.GetCurrentProcessToken())
	if !ok {
		return ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		// A medium process cannot open the token of an elevated one: access denied is itself the answer.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return windowText(hwnd)
		}
		return ""
	}
	defer tok.Close()
	if level, ok := tokenIntegrity(tok); ok && level > own {
		return windowText(hwnd)
	}
	return ""
}

// tokenIntegrity returns the integrity level (0x2000 medium, 0x3000 high) of a token.
func tokenIntegrity(tok windows.Token) (uint32, bool) {
	var size uint32
	_ = windows.GetTokenInformation(tok, windows.TokenIntegrityLevel, nil, 0, &size)
	if size == 0 {
		return 0, false
	}
	buf := make([]byte, size)
	if err := windows.GetTokenInformation(tok, windows.TokenIntegrityLevel, &buf[0], size, &size); err != nil {
		return 0, false
	}
	label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buf[0]))
	sid := label.Label.Sid
	count := sid.SubAuthorityCount()
	if count == 0 {
		return 0, false
	}
	return sid.SubAuthority(uint32(count - 1)), true
}
