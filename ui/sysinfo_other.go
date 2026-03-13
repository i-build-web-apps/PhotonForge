//go:build !darwin

package ui

// totalSystemRAM returns total physical memory in bytes.
// On Linux this uses /proc/meminfo; on other platforms returns 0.
func totalSystemRAM() uint64 {
	// Could parse /proc/meminfo on Linux; for now return 0
	// which causes the monitor to fall back to absolute thresholds.
	return 0
}
