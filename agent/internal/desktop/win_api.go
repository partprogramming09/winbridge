//go:build windows

package desktop

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ntdll    = syscall.NewLazyDLL("ntdll.dll")
	iphlpapi = syscall.NewLazyDLL("iphlpapi.dll")

	wtsapi32 = syscall.NewLazyDLL("wtsapi32.dll")

	pWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")
	pWTSFreeMemory               = wtsapi32.NewProc("WTSFreeMemory")

	pGetAsyncKeyState          = user32.NewProc("GetAsyncKeyState")
	pGetKeyboardLayout         = user32.NewProc("GetKeyboardLayout")
	pMapVirtualKeyExW          = user32.NewProc("MapVirtualKeyExW")
	pGetWindowThreadProcessId  = user32.NewProc("GetWindowThreadProcessId")
	pOpenInputDesktop          = user32.NewProc("OpenInputDesktop")
	pCloseDesktop              = user32.NewProc("CloseDesktop")
	pGetUserObjectInformationW = user32.NewProc("GetUserObjectInformationW")
	pGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	pSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware")
	pGetDC                     = user32.NewProc("GetDC")
	pReleaseDC                 = user32.NewProc("ReleaseDC")
	pEnumWindows               = user32.NewProc("EnumWindows")
	pEnumChildWindows          = user32.NewProc("EnumChildWindows")
	pEnumDisplayMonitors       = user32.NewProc("EnumDisplayMonitors")
	pIsWindowVisible           = user32.NewProc("IsWindowVisible")
	pIsIconic                  = user32.NewProc("IsIconic")
	pShowWindow                = user32.NewProc("ShowWindow")
	pSetForegroundWindow       = user32.NewProc("SetForegroundWindow")
	pGetForegroundWindow       = user32.NewProc("GetForegroundWindow")
	pGetWindowTextW            = user32.NewProc("GetWindowTextW")
	pGetWindowTextLengthW      = user32.NewProc("GetWindowTextLengthW")
	pGetClassNameW             = user32.NewProc("GetClassNameW")
	pGetWindowRect             = user32.NewProc("GetWindowRect")
	pSetCursorPos              = user32.NewProc("SetCursorPos")
	pGetCursorPos              = user32.NewProc("GetCursorPos")
	pSendInput                 = user32.NewProc("SendInput")
	pCreateCompatibleDC        = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap    = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject              = gdi32.NewProc("SelectObject")
	pBitBlt                    = gdi32.NewProc("BitBlt")
	pGetDIBits                 = gdi32.NewProc("GetDIBits")
	pDeleteObject              = gdi32.NewProc("DeleteObject")
	pDeleteDC                  = gdi32.NewProc("DeleteDC")
	pGetUserDefaultLocale      = kernel32.NewProc("GetUserDefaultLocaleName")
	pGlobalMemoryStatusEx      = kernel32.NewProc("GlobalMemoryStatusEx")
	pGetDiskFreeSpaceExW       = kernel32.NewProc("GetDiskFreeSpaceExW")
	pGetSystemTimes            = kernel32.NewProc("GetSystemTimes")
	pGetTickCount64            = kernel32.NewProc("GetTickCount64")
	pRtlMoveMemory             = kernel32.NewProc("RtlMoveMemory")
	pRtlGetVersion             = ntdll.NewProc("RtlGetVersion")
	pGetIfTable2               = iphlpapi.NewProc("GetIfTable2")
	pFreeMibTable              = iphlpapi.NewProc("FreeMibTable")
	smXVirtual, smYVirtual     = 76, 77
	smCXVirtual, smCYVirtual   = 78, 79
)

const (
	srcCopy    = 0x00CC0020
	captureBlt = 0x40000000
	swRestore  = 9

	inputMouse    = 0
	inputKeyboard = 1

	mouseMove        = 0x0001
	mouseLeftDown    = 0x0002
	mouseLeftUp      = 0x0004
	mouseRightDown   = 0x0008
	mouseRightUp     = 0x0010
	mouseMiddleDown  = 0x0020
	mouseMiddleUp    = 0x0040
	mouseWheel       = 0x0800
	mouseHWheel      = 0x1000
	keyExtended      = 0x0001
	keyUp            = 0x0002
	keyUnicode       = 0x0004
	vkReturn         = 0x0D
	vkTab            = 0x09
	vkMenu           = 0x12
	inputStructBytes = 40
)

// rect is the Win32 RECT.
type rect struct{ Left, Top, Right, Bottom int32 }

// point is the Win32 POINT.
type point struct{ X, Y int32 }

// input is the Win32 INPUT: a type, padding to 8 bytes, and a 32-byte union filled by the helpers below.
type input struct {
	Type uint32
	_    uint32
	U    [32]byte
}

// mouseInput builds a MOUSEINPUT event.
func mouseInput(flags uint32, data int32) input {
	in := input{Type: inputMouse}
	binary.LittleEndian.PutUint32(in.U[8:12], uint32(data))
	binary.LittleEndian.PutUint32(in.U[12:16], flags)
	return in
}

// keyInput builds a KEYBDINPUT event.
func keyInput(vk, scan uint16, flags uint32) input {
	in := input{Type: inputKeyboard}
	binary.LittleEndian.PutUint16(in.U[0:2], vk)
	binary.LittleEndian.PutUint16(in.U[2:4], scan)
	binary.LittleEndian.PutUint32(in.U[4:8], flags)
	return in
}

// sendInputs injects the events; it fails if Windows accepted fewer than asked (for example a blocked desktop).
func sendInputs(events ...input) error {
	if len(events) == 0 {
		return nil
	}
	if unsafe.Sizeof(events[0]) != inputStructBytes {
		return fmt.Errorf("INPUT is %d bytes, expected %d", unsafe.Sizeof(events[0]), inputStructBytes)
	}
	sent, _, err := pSendInput.Call(uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), unsafe.Sizeof(events[0]))
	if int(sent) != len(events) {
		return fmt.Errorf("SendInput accepted %d of %d events (the desktop may be locked or protected): %v", sent, len(events), err)
	}
	return nil
}

// windowText returns the title of a window.
func windowText(hwnd uintptr) string {
	n, _, _ := pGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

// className returns the window class of a window.
func className(hwnd uintptr) string {
	buf := make([]uint16, 256)
	n, _, _ := pGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}

// windowRect returns the screen rectangle of a window.
func windowRect(hwnd uintptr) (rect, bool) {
	var r rect
	ok, _, _ := pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r, ok != 0
}

// visible reports whether a window is visible.
func visible(hwnd uintptr) bool {
	ok, _, _ := pIsWindowVisible.Call(hwnd)
	return ok != 0
}
