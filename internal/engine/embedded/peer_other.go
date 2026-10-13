//go:build !darwin && !linux

package embedded

import "net"

// peerPID is unknown here: the token alone decides.
func peerPID(net.Conn) (int, bool) { return 0, false }

func parentPID(int) (int, bool) { return 0, false }

func procStart(int) string { return "" }
