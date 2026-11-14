//go:build windows

package diag

import (
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	pGetCurrentProcess     = kernel32.NewProc("GetCurrentProcess")
	pGetProcessMemoryInfo  = kernel32.NewProc("K32GetProcessMemoryInfo")
	pGetProcessHandleCount = kernel32.NewProc("GetProcessHandleCount")
)

// processMemoryCountersEx is PROCESS_MEMORY_COUNTERS_EX.
type processMemoryCountersEx struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

// processMemory returns the working set, the private bytes and the handle count of this process.
func processMemory() map[string]any {
	out := map[string]any{}
	proc, _, _ := pGetCurrentProcess.Call()
	var c processMemoryCountersEx
	c.CB = uint32(unsafe.Sizeof(c))
	if ok, _, _ := pGetProcessMemoryInfo.Call(proc, uintptr(unsafe.Pointer(&c)), uintptr(c.CB)); ok != 0 {
		out["working_set_bytes"] = uint64(c.WorkingSetSize)
		out["private_bytes"] = uint64(c.PrivateUsage)
	}
	var handles uint32
	if ok, _, _ := pGetProcessHandleCount.Call(proc, uintptr(unsafe.Pointer(&handles))); ok != 0 {
		out["handles"] = handles
	}
	return out
}
