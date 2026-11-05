//go:build !linux

package db

// physicalMemory is unknown off Linux; the automatic bulk heap limit is then off.
func physicalMemory() uint64 { return 0 }
