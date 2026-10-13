package embedded

import (
	"net"
	"strconv"

	"golang.org/x/sys/unix"
)

// peerPID is the PID of the process at the other end of a Unix socket.
func peerPID(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	pid, serr := 0, error(nil)
	if err := raw.Control(func(fd uintptr) { pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID) }); err != nil || serr != nil {
		return 0, false
	}

	return pid, pid > 0
}

// parentPID is a process's parent's PID.
func parentPID(pid int) (int, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return 0, false
	}

	return int(kp.Eproc.Ppid), true
}

// procStart is when a process started, which tells it from a later process
// with its PID.
func procStart(pid int) string {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ""
	}

	return strconv.FormatInt(kp.Proc.P_starttime.Sec, 10) + "." + strconv.FormatInt(int64(kp.Proc.P_starttime.Usec), 10)
}
