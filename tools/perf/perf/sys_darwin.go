package perf

import (
	"os"
	"syscall"
	"unsafe"
)

// rusageInfoV2 is the start of struct rusage_info_v2 (sys/resource.h).
type rusageInfoV2 struct {
	UUID                [16]byte
	UserTime            uint64
	SystemTime          uint64
	PkgIdleWkups        uint64
	InterruptWkups      uint64
	Pageins             uint64
	WiredSize           uint64
	ResidentSize        uint64
	PhysFootprint       uint64
	ProcStartAbstime    uint64
	ProcExitAbstime     uint64
	ChildUserTime       uint64
	ChildSystemTime     uint64
	ChildPkgIdleWkups   uint64
	ChildInterruptWkups uint64
	ChildPageins        uint64
	ChildElapsedAbstime uint64
	DiskioBytesread     uint64
	DiskioByteswritten  uint64
}

// proc_pid_rusage is the proc_info system call with PROC_INFO_CALL_PIDRUSAGE.
const (
	sysProcInfo        = 336
	procInfoPIDRusage  = 9
	rusageInfoVersion2 = 2
)

// diskAndWakeups are the bytes the process wrote to disk and its idle and
// interrupt wakeups, from proc_pid_rusage.
func diskAndWakeups(syscall.Rusage) (disk, wakeups uint64) {
	var ri rusageInfoV2
	_, _, errno := syscall.Syscall6(sysProcInfo, procInfoPIDRusage, uintptr(os.Getpid()), rusageInfoVersion2, 0, uintptr(unsafe.Pointer(&ri)), 0)
	if errno != 0 {
		return 0, 0
	}

	return ri.DiskioByteswritten, ri.PkgIdleWkups + ri.InterruptWkups
}

// osMemory is the memory the system charges the process: its physical
// footprint, which Activity Monitor shows. Pages the Go runtime returned
// stay resident until the system needs them, but leave the footprint.
func osMemory() uint64 {
	var ri rusageInfoV2
	_, _, errno := syscall.Syscall6(sysProcInfo, procInfoPIDRusage, uintptr(os.Getpid()), rusageInfoVersion2, 0, uintptr(unsafe.Pointer(&ri)), 0)
	if errno != 0 {
		return 0
	}

	return ri.PhysFootprint
}
