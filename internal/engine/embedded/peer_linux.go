package embedded

import (
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// peerPID is the PID of the process at the other end of a Unix socket, in
// this process's PID namespace.
func peerPID(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) { cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || serr != nil {
		return 0, false
	}

	return int(cred.Pid), cred.Pid > 0
}

// parentPID is a process's parent's PID, from /proc/<pid>/stat.
func parentPID(pid int) (int, bool) {
	fields := statFields(pid)
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])

	return ppid, err == nil
}

// procStart is when a process started (field 22 of /proc/<pid>/stat, in
// clock ticks), which tells it from a later process with its PID.
func procStart(pid int) string {
	fields := statFields(pid)
	if len(fields) < 20 {
		return ""
	}

	return fields[19]
}

// statFields are the fields of /proc/<pid>/stat after the command's name,
// which is in parentheses and may hold spaces: the state is the first.
func statFields(pid int) []string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil
	}
	_, rest, ok := strings.CutLast(string(data), ")")
	if !ok {
		return nil
	}

	return strings.Fields(rest)
}
