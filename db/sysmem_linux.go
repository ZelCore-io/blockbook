//go:build linux

package db

import "syscall"

// physicalMemory returns the host RAM in bytes, 0 when unknown.
func physicalMemory() uint64 {
	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err != nil {
		return 0
	}
	return uint64(si.Totalram) * uint64(si.Unit)
}
