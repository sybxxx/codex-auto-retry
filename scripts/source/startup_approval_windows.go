//go:build windows

package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const startupApprovedRunSubKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
const startupRunSubKey = `Software\Microsoft\Windows\CurrentVersion\Run`

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

// ensureStartupApproval repairs only the plugin's own marker when the matching
// Run entry points at this executable. Windows can preserve the Run value while
// independently disabling StartupApproved after an update or crash; leaving
// that mismatch silently breaks the next sign-in.
func ensureStartupApproval(executable string) error {
	if strings.TrimSpace(executable) == "" {
		return nil
	}
	runKey, err := registry.OpenKey(registry.CURRENT_USER, startupRunSubKey, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	value, _, valueErr := runKey.GetStringValue("CodexAutoRetry")
	runKey.Close()
	if valueErr != nil || !startupEntryMatchesExecutable(value, executable) {
		return nil
	}
	approvedKey, err := registry.OpenKey(registry.CURRENT_USER, startupApprovedRunSubKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		approvedKey, _, err = registry.CreateKey(registry.CURRENT_USER, startupApprovedRunSubKey, registry.SET_VALUE)
		if err != nil {
			return err
		}
	}
	defer approvedKey.Close()
	bytes, _, readErr := approvedKey.GetBinaryValue("CodexAutoRetry")
	if readErr != nil || len(bytes) < 12 {
		bytes = make([]byte, 12)
	}
	if len(bytes) < 4 || bytes[0] == 2 && bytes[1] == 0 && bytes[2] == 0 && bytes[3] == 0 {
		return nil
	}
	bytes[0], bytes[1], bytes[2], bytes[3] = 2, 0, 0, 0
	return approvedKey.SetBinaryValue("CodexAutoRetry", bytes)
}

func startupEntryMatchesExecutable(value, executable string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	path := value
	if strings.HasPrefix(path, `"`) {
		if end := strings.Index(path[1:], `"`); end >= 0 {
			path = path[1 : end+1]
		}
	} else if fields := strings.Fields(path); len(fields) > 0 {
		path = fields[0]
	}
	left, leftErr := filepath.Abs(filepath.Clean(path))
	right, rightErr := filepath.Abs(filepath.Clean(executable))
	return leftErr == nil && rightErr == nil && strings.EqualFold(left, right)
}
