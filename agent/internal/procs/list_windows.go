//go:build windows

package procs

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// List returns the running processes with their parent, session and, when readable, their executable path.
func List() ([]Process, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var out []Process
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		p := Process{PID: int(entry.ProcessID), PPID: int(entry.ParentProcessID), Name: windows.UTF16ToString(entry.ExeFile[:])}
		var session uint32
		if windows.ProcessIdToSessionId(entry.ProcessID, &session) == nil {
			p.Session = int(session)
		}
		p.Path = imagePath(entry.ProcessID)
		out = append(out, p)
	}
	return out, nil
}

// imagePath returns the full path of a process executable, or "" when access is denied.
func imagePath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH*4)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}
