package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func appendMemoryGuardLog(dataDir, component string, sample memorySample, limitMB int) {
	path := filepath.Join(dataDir, "logs", "daemon.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintf(file, "%s memory guard triggered component=%s pid=%d private_memory_mb=%d limit_mb=%d\n",
		sample.CheckedAt.UTC().Format("2006/01/02 15:04:05"), component, os.Getpid(), memoryBytesToMB(sample.PrivateBytes), limitMB)
}
