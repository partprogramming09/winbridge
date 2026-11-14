//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/partprogramming09/winbridge/agent/internal/imaging"
)

// New returns the Windows desktop. The process is made DPI aware so coordinates are physical pixels.
func New() (Desktop, error) {
	pSetProcessDPIAware.Call()
	return win{}, nil
}

type win struct{}

// monitors returns the rectangle of every display monitor in enumeration order.
func monitors() []rect {
	var out []rect
	cb := syscall.NewCallback(func(_, _, r, _ uintptr) uintptr {
		var rc rect
		copyFrom(r, unsafe.Pointer(&rc), unsafe.Sizeof(rc))
		out = append(out, rc)
		return 1
	})
	pEnumDisplayMonitors.Call(0, 0, cb, 0)
	return out
}

// metric reads one system metric.
func metric(index int) int {
	v, _, _ := pGetSystemMetrics.Call(uintptr(index))
	return int(int32(v))
}

// Screenshot captures one monitor, or the whole virtual screen, and returns it as JPEG.
func (win) Screenshot(o ScreenshotOptions) ([]byte, error) {
	area := rect{int32(metric(smXVirtual)), int32(metric(smYVirtual)),
		int32(metric(smXVirtual) + metric(smCXVirtual)), int32(metric(smYVirtual) + metric(smCYVirtual))}
	if o.Monitor > 0 {
		list := monitors()
		if o.Monitor > len(list) {
			return nil, fmt.Errorf("monitor %d does not exist; there are %d", o.Monitor, len(list))
		}
		area = list[o.Monitor-1]
	}
	if r := o.Region; r != nil {
		whole := area
		area = rect{int32(r.X), int32(r.Y), int32(r.X + r.Width), int32(r.Y + r.Height)}
		if area.Left < whole.Left || area.Top < whole.Top || area.Right > whole.Right || area.Bottom > whole.Bottom {
			return nil, fmt.Errorf("region %d,%d,%d,%d is outside the captured area", r.X, r.Y, r.Width, r.Height)
		}
	}
	width, height := int(area.Right-area.Left), int(area.Bottom-area.Top)
	if width <= 0 || height <= 0 {
		return nil, errors.New("the screen has no size")
	}
	pixels, err := grab(int(area.Left), int(area.Top), width, height)
	if err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < len(pixels); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = pixels[i+2], pixels[i+1], pixels[i], 255
	}
	return imaging.EncodeJPEG(imaging.Downscale(img, o.MaxWidth), o.Quality)
}

// bitmapInfoHeader is the Win32 BITMAPINFOHEADER.
type bitmapInfoHeader struct {
	Size                   uint32
	Width, Height          int32
	Planes, BitCount       uint16
	Compression, SizeImage uint32
	XPels, YPels           int32
	ClrUsed, ClrImportant  uint32
}

// grab copies a screen area into a BGRA byte slice.
func grab(left, top, width, height int) ([]byte, error) {
	screen, _, _ := pGetDC.Call(0)
	if screen == 0 {
		return nil, errors.New("GetDC failed: there is no desktop in this session")
	}
	defer pReleaseDC.Call(0, screen)
	mem, _, _ := pCreateCompatibleDC.Call(screen)
	if mem == 0 {
		return nil, errors.New("CreateCompatibleDC failed")
	}
	defer pDeleteDC.Call(mem)
	bmp, _, _ := pCreateCompatibleBitmap.Call(screen, uintptr(width), uintptr(height))
	if bmp == 0 {
		return nil, errors.New("CreateCompatibleBitmap failed")
	}
	defer pDeleteObject.Call(bmp)
	pSelectObject.Call(mem, bmp)
	if ok, _, _ := pBitBlt.Call(mem, 0, 0, uintptr(width), uintptr(height), screen,
		uintptr(int32(left)), uintptr(int32(top)), srcCopy|captureBlt); ok == 0 {
		return nil, errors.New("BitBlt failed: the screen could not be copied (locked or protected desktop)")
	}
	header := bitmapInfoHeader{Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(width), Height: -int32(height), Planes: 1, BitCount: 32}
	pixels := make([]byte, width*height*4)
	if n, _, _ := pGetDIBits.Call(mem, bmp, 0, uintptr(height), uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&header)), 0); n == 0 {
		return nil, errors.New("GetDIBits failed")
	}
	return pixels, nil
}

// Windows lists the visible top-level windows that have a title.
func (win) Windows() ([]Window, error) {
	var out []Window
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		if !visible(hwnd) {
			return 1
		}
		title := windowText(hwnd)
		if title == "" {
			return 1
		}
		if r, ok := windowRect(hwnd); ok {
			out = append(out, Window{Handle: int64(hwnd), Title: title,
				Left: int(r.Left), Top: int(r.Top), Right: int(r.Right), Bottom: int(r.Bottom)})
		}
		return 1
	})
	pEnumWindows.Call(cb, 0)
	return out, nil
}

// Elements lists the visible child controls of the foreground window.
func (win) Elements() ([]Element, error) {
	fg, _, _ := pGetForegroundWindow.Call()
	if fg == 0 {
		return nil, nil
	}
	var out []Element
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		if !visible(hwnd) {
			return 1
		}
		if r, ok := windowRect(hwnd); ok {
			out = append(out, Element{Index: len(out) + 1, Class: className(hwnd), Text: windowText(hwnd),
				Left: int(r.Left), Top: int(r.Top), Right: int(r.Right), Bottom: int(r.Bottom)})
		}
		return 1
	})
	pEnumChildWindows.Call(fg, cb, 0)
	return out, nil
}

// SystemLanguage returns the display language like en_US.
func (win) SystemLanguage() string {
	buf := make([]uint16, 85)
	n, _, _ := pGetUserDefaultLocale.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return "en_US"
	}
	return strings.ReplaceAll(syscall.UTF16ToString(buf), "-", "_")
}

// cursor returns the pointer position.
func cursor() (int, int) {
	var p point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return int(p.X), int(p.Y)
}

// setCursor moves the pointer instantly.
func setCursor(x, y int) error {
	if ok, _, err := pSetCursorPos.Call(uintptr(int32(x)), uintptr(int32(y))); ok == 0 {
		return fmt.Errorf("SetCursorPos failed: %v", err)
	}
	return nil
}

// buttons maps a button name to its down and up flags.
var buttons = map[string][2]uint32{
	"left":   {mouseLeftDown, mouseLeftUp},
	"right":  {mouseRightDown, mouseRightUp},
	"middle": {mouseMiddleDown, mouseMiddleUp},
}

// Click moves to the point and presses the button once, twice, or not at all (hover).
func (win) Click(x, y int, button, action string) error {
	if err := setCursor(x, y); err != nil {
		return err
	}
	if action == "hover" {
		return nil
	}
	flags, ok := buttons[button]
	if !ok {
		return fmt.Errorf("unknown button %q", button)
	}
	press := func() error { return sendInputs(mouseInput(flags[0], 0), mouseInput(flags[1], 0)) }
	if err := press(); err != nil {
		return err
	}
	if action == "double" {
		time.Sleep(50 * time.Millisecond)
		return press()
	}
	return nil
}

// MoveTo glides the pointer to a point over the given time.
func (win) MoveTo(x, y int, d time.Duration) error {
	if d <= 0 {
		return setCursor(x, y)
	}
	sx, sy := cursor()
	const step = 8 * time.Millisecond
	steps := int(d / step)
	for i := 1; i <= steps; i++ {
		f := float64(i) / float64(steps)
		if err := setCursor(sx+int(float64(x-sx)*f), sy+int(float64(y-sy)*f)); err != nil {
			return err
		}
		time.Sleep(step)
	}
	return setCursor(x, y)
}

// Drag holds the left button while moving to the target.
func (w win) Drag(startX, startY int, hasStart bool, x, y int, d time.Duration) error {
	if hasStart {
		if err := setCursor(startX, startY); err != nil {
			return err
		}
	}
	if err := sendInputs(mouseInput(mouseLeftDown, 0)); err != nil {
		return err
	}
	moveErr := w.MoveTo(x, y, d)
	upErr := sendInputs(mouseInput(mouseLeftUp, 0))
	if moveErr != nil {
		return moveErr
	}
	return upErr
}

// Scroll turns the wheel by amount units (the raw value, like the Python server).
func (win) Scroll(amount int, horizontal bool) error {
	flag := uint32(mouseWheel)
	if horizontal {
		flag = mouseHWheel
	}
	return sendInputs(mouseInput(flag, int32(amount)))
}

// TypeText delivers text by pasting it, or one Unicode input event per character; see TypeMode.
func (win) TypeText(text string, mode TypeMode) error {
	if mode == TypePaste && text != "" {
		if used, err := pasteText(text); err != nil {
			return err
		} else if used {
			return nil
		}
	}
	return typeKeys(text)
}

// keyPace is the pause after each typed character: applications that resolve Unicode input late repeat the last
// character when events arrive faster than they are read.
const keyPace = 12 * time.Millisecond

// typeKeys types text as Unicode characters; newline and tab become Enter and Tab.
func typeKeys(text string) error {
	for _, r := range text {
		var events []input
		switch r {
		case '\n':
			events = []input{keyInput(vkReturn, 0, 0), keyInput(vkReturn, 0, keyUp)}
		case '\r':
			continue
		case '\t':
			events = []input{keyInput(vkTab, 0, 0), keyInput(vkTab, 0, keyUp)}
		default:
			for _, unit := range utf16.Encode([]rune{r}) {
				events = append(events, keyInput(0, unit, keyUnicode), keyInput(0, unit, keyUnicode|keyUp))
			}
		}
		if err := sendInputs(events...); err != nil {
			return err
		}
		time.Sleep(keyPace)
	}
	return nil
}

// Hotkey presses the keys in order and releases them in reverse.
func (win) Hotkey(keys []string) error {
	layout := foregroundLayout()
	var down, up []input
	for _, k := range keys {
		vk, extended, ok := virtualKey(k)
		if !ok {
			return fmt.Errorf("unknown key %q", k)
		}
		flags := uint32(0)
		if extended {
			flags = keyExtended
		}
		sc, _, _ := pMapVirtualKeyExW.Call(uintptr(vk), 0, layout)
		down = append(down, keyInput(vk, uint16(sc), flags))
		up = append([]input{keyInput(vk, uint16(sc), flags|keyUp)}, up...)
	}
	return sendInputs(append(down, up...)...)
}

// PressKey presses and releases one key.
func (w win) PressKey(key string) error { return w.Hotkey([]string{key}) }

// virtualKey maps a key name to its virtual-key code and whether it is an extended key.
func virtualKey(name string) (uint16, bool, bool) {
	named := map[string]struct {
		vk       uint16
		extended bool
	}{
		"ctrl": {0x11, false}, "control": {0x11, false}, "alt": {0x12, false}, "shift": {0x10, false},
		"win": {0x5B, true}, "windows": {0x5B, true}, "cmd": {0x5B, true},
		"enter": {0x0D, false}, "return": {0x0D, false}, "tab": {0x09, false}, "esc": {0x1B, false}, "escape": {0x1B, false},
		"space": {0x20, false}, "backspace": {0x08, false}, "delete": {0x2E, true}, "del": {0x2E, true},
		"insert": {0x2D, true}, "home": {0x24, true}, "end": {0x23, true}, "pageup": {0x21, true}, "pagedown": {0x22, true},
		"left": {0x25, true}, "up": {0x26, true}, "right": {0x27, true}, "down": {0x28, true},
		"printscreen": {0x2C, true}, "capslock": {0x14, false},
		"-": {0xBD, false}, "=": {0xBB, false}, ",": {0xBC, false}, ".": {0xBE, false}, "/": {0xBF, false},
		";": {0xBA, false}, "'": {0xDE, false}, "[": {0xDB, false}, "]": {0xDD, false}, "\\": {0xDC, false}, "`": {0xC0, false},
	}
	name = strings.ToLower(name)
	if k, ok := named[name]; ok {
		return k.vk, k.extended, true
	}
	if len(name) == 1 {
		c := name[0]
		switch {
		case c >= 'a' && c <= 'z':
			return uint16(c-'a') + 0x41, false, true
		case c >= '0' && c <= '9':
			return uint16(c-'0') + 0x30, false, true
		}
	}
	if len(name) >= 2 && name[0] == 'f' {
		n := 0
		for _, d := range name[1:] {
			if d < '0' || d > '9' {
				return 0, false, false
			}
			n = n*10 + int(d-'0')
		}
		if n >= 1 && n <= 24 {
			return uint16(0x70 + n - 1), false, true
		}
	}
	return 0, false, false
}

// Focus restores a minimized window and brings it to the front.
func (win) Focus(handle int64) (string, error) {
	hwnd := uintptr(handle)
	if !visible(hwnd) && windowText(hwnd) == "" {
		return "", fmt.Errorf("window %d does not exist", handle)
	}
	if iconic, _, _ := pIsIconic.Call(hwnd); iconic != 0 {
		pShowWindow.Call(hwnd, swRestore)
	}
	// A bare Alt press lets SetForegroundWindow through Windows' foreground lock.
	_ = sendInputs(keyInput(vkMenu, 0, 0), keyInput(vkMenu, 0, keyUp))
	if ok, _, err := pSetForegroundWindow.Call(hwnd); ok == 0 {
		return "", fmt.Errorf("SetForegroundWindow refused: %v", err)
	}
	return windowText(hwnd), nil
}

// SystemInfo reports the same lines as the Python server.
func (win) SystemInfo() (string, error) {
	cpu := cpuPercent(500 * time.Millisecond)
	var mem struct {
		Length                              uint32
		Load                                uint32
		TotalPhys, AvailPhys                uint64
		TotalPage, AvailPage                uint64
		TotalVirtual, AvailVirtual, AvailEx uint64
	}
	mem.Length = uint32(unsafe.Sizeof(mem))
	if ok, _, err := pGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&mem))); ok == 0 {
		return "", fmt.Errorf("GlobalMemoryStatusEx failed: %v", err)
	}
	var free, total, totalFree uint64
	root, _ := syscall.UTF16PtrFromString(`C:\`)
	if ok, _, err := pGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(root)), uintptr(unsafe.Pointer(&free)),
		uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree))); ok == 0 {
		return "", fmt.Errorf("GetDiskFreeSpaceEx failed: %v", err)
	}
	sent, recv := networkBytes()
	ticks, _, _ := pGetTickCount64.Call()
	uptime := time.Duration(ticks) * time.Millisecond
	boot := time.Now().Add(-uptime)
	usedDisk := total - totalFree
	lines := []string{
		fmt.Sprintf("**System:** Windows %s (%s)", windowsRelease(), strings.ToUpper(strings.Replace(runtime.GOARCH, "amd64", "AMD64", 1))),
		fmt.Sprintf("**CPU:** %.1f%% (%d cores)", cpu, runtime.NumCPU()),
		fmt.Sprintf("**Memory:** %d.%d%% — %dMB / %dMB", mem.Load, 0, (mem.TotalPhys-mem.AvailPhys)>>20, mem.TotalPhys>>20),
		fmt.Sprintf("**Disk (C:):** %.1f%% — %dGB / %dGB", 100*float64(usedDisk)/float64(total), usedDisk>>30, total>>30),
		fmt.Sprintf("**Network:** Sent %dMB / Recv %dMB", sent>>20, recv>>20),
		fmt.Sprintf("**Uptime:** %s (boot: %s)", formatUptime(uptime), boot.Format("2006-01-02 15:04")),
	}
	return strings.Join(lines, "\n"), nil
}

// cpuPercent samples the system times twice, d apart.
func cpuPercent(d time.Duration) float64 {
	read := func() (idle, kernel, user uint64) {
		var i, k, u syscall.Filetime
		pGetSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
		ft := func(f syscall.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
		return ft(i), ft(k), ft(u)
	}
	i1, k1, u1 := read()
	time.Sleep(d)
	i2, k2, u2 := read()
	total := (k2 - k1) + (u2 - u1)
	if total == 0 {
		return 0
	}
	return 100 * (1 - float64(i2-i1)/float64(total))
}

// windowsRelease returns "10" or "11" from the OS build number.
func windowsRelease() string {
	var v struct {
		Size                uint32
		Major, Minor, Build uint32
		Platform            uint32
		CSD                 [128]uint16
	}
	v.Size = uint32(unsafe.Sizeof(v))
	pRtlGetVersion.Call(uintptr(unsafe.Pointer(&v)))
	if v.Build >= 22000 {
		return "11"
	}
	return fmt.Sprintf("%d", v.Major)
}

// networkBytes sums the sent and received bytes of every non-loopback, non-filter interface (MIB_IF_ROW2 InOctets/OutOctets).
func networkBytes() (sent, recv uint64) {
	const (
		rowSize, inOctets, outOctets = 1352, 1208, 1280
		ifTypeOffset                 = 1128
		statusFlagsOffset            = 1152
		ifTypeLoopback               = 24
		filterInterfaceBit           = 0x02
	)
	var table uintptr
	if r, _, _ := pGetIfTable2.Call(uintptr(unsafe.Pointer(&table))); r != 0 || table == 0 {
		return 0, 0
	}
	defer pFreeMibTable.Call(table)
	var count uint32
	copyFrom(table, unsafe.Pointer(&count), unsafe.Sizeof(count))
	base := table + 8
	for i := uintptr(0); i < uintptr(count); i++ {
		row := base + i*rowSize
		var ifType uint32
		var flags uint8
		copyFrom(row+ifTypeOffset, unsafe.Pointer(&ifType), unsafe.Sizeof(ifType))
		copyFrom(row+statusFlagsOffset, unsafe.Pointer(&flags), unsafe.Sizeof(flags))
		if ifType == ifTypeLoopback || flags&filterInterfaceBit != 0 {
			continue
		}
		var in, out uint64
		copyFrom(row+inOctets, unsafe.Pointer(&in), unsafe.Sizeof(in))
		copyFrom(row+outOctets, unsafe.Pointer(&out), unsafe.Sizeof(out))
		recv += in
		sent += out
	}
	return sent, recv
}

// copyFrom copies n bytes from a memory address owned by Windows into dst.
func copyFrom(addr uintptr, dst unsafe.Pointer, n uintptr) {
	pRtlMoveMemory.Call(uintptr(dst), addr, n)
}

// formatUptime prints a duration the way Python's timedelta does: "6:43:55" or "1 day, 2:03:04".
func formatUptime(d time.Duration) string {
	total := int(d.Seconds())
	days, rest := total/86400, total%86400
	clock := fmt.Sprintf("%d:%02d:%02d", rest/3600, rest%3600/60, rest%60)
	switch days {
	case 0:
		return clock
	case 1:
		return "1 day, " + clock
	}
	return fmt.Sprintf("%d days, %s", days, clock)
}

// SessionState reads the name of the desktop that receives input. The interactive desktop is Default; the lock
// screen is Winlogon; when the desktop cannot be opened a secure desktop (a UAC prompt) is in front. It is read on
// every call because the state changes at any moment.
func (win) SessionState() SessionState {
	const desktopReadObjects, uoiName = 0x0001, 2
	h, _, _ := pOpenInputDesktop.Call(0, 0, desktopReadObjects)
	if h == 0 {
		return SessionState{Locked: true, Desktop: "secure"}
	}
	defer pCloseDesktop.Call(h)
	buf := make([]uint16, 128)
	var needed uint32
	ok, _, _ := pGetUserObjectInformationW.Call(h, uoiName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		return SessionState{Locked: true, Desktop: "unknown"}
	}
	name := syscall.UTF16ToString(buf)
	if name == "Default" && sessionLockedByWTS() {
		// Windows 11 draws the lock screen on the Default desktop, so the desktop name alone cannot tell.
		return SessionState{Locked: true, Desktop: "Default (lock screen)"}
	}
	return SessionState{Locked: name != "Default", Desktop: name, ElevatedForeground: elevatedForegroundTitle()}
}

// sessionLockedByWTS asks the terminal service whether the current session is locked. It is false when the answer is
// not available, so a failure never blocks input that would have worked.
func sessionLockedByWTS() bool {
	const (
		wtsCurrentSession = 0xFFFFFFFF
		wtsSessionInfoEx  = 25
		flagsOffset       = 16 // WTSINFOEXW: Level (4) + padding, then SessionId, SessionState, SessionFlags
		wtsSessionLock    = 0
	)
	var data unsafe.Pointer // the service allocates the answer and writes its address here
	var size uint32
	ok, _, _ := pWTSQuerySessionInformationW.Call(0, wtsCurrentSession, wtsSessionInfoEx, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || data == nil {
		return false
	}
	defer pWTSFreeMemory.Call(uintptr(data))
	if size < flagsOffset+4 {
		return false
	}
	flags := *(*uint32)(unsafe.Add(data, flagsOffset))
	return flags == wtsSessionLock
}

// foregroundLayout returns the keyboard layout of the thread that owns the foreground window.
func foregroundLayout() uintptr {
	hwnd, _, _ := pGetForegroundWindow.Call()
	tid, _, _ := pGetWindowThreadProcessId.Call(hwnd, 0)
	layout, _, _ := pGetKeyboardLayout.Call(tid)
	return layout
}

// HeldModifiers reads the asynchronous state of the modifier keys; a key that stays here after an input call means a
// release event was lost.
func (win) HeldModifiers() []string {
	keys := []struct {
		name string
		vk   uintptr
	}{{"shift", 0x10}, {"ctrl", 0x11}, {"alt", 0x12}, {"win", 0x5B}, {"win", 0x5C}}
	held := []string{}
	for _, k := range keys {
		if state, _, _ := pGetAsyncKeyState.Call(k.vk); state&0x8000 != 0 {
			held = append(held, k.name)
		}
	}
	return held
}
