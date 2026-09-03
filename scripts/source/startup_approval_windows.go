//go:build windows

package main

import "golang.org/x/sys/windows/registry"

const startupApprovedRunSubKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`

// readStartupApprovalStatus reads the Windows approval marker without changing
// it. The Run command and this marker are independent registry values; both
// must be healthy for sign-in startup to be reliable.
func readStartupApprovalStatus() string {
	key, err := registry.OpenKey(registry.CURRENT_USER, startupApprovedRunSubKey, registry.QUERY_VALUE)
	if err != nil {
		return "unknown"
	}
	defer key.Close()
	bytes, _, err := key.GetBinaryValue("CodexAutoRetry")
	if err != nil || len(bytes) < 4 || bytes[1] != 0 || bytes[2] != 0 || bytes[3] != 0 {
		return "unknown"
	}
	switch bytes[0] {
	case 2:
		return "enabled"
	case 3:
		return "disabled"
	default:
		return "unknown"
	}
}
