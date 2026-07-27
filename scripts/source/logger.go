package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

type safeLogger struct {
	mu     sync.Mutex
	file   *os.File
	logger *log.Logger
	path   string
}

func newSafeLogger(path string) (*safeLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 5*1024*1024 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &safeLogger{
		file:   file,
		logger: log.New(io.Writer(file), "", log.Ldate|log.Ltime|log.LUTC),
		path:   path,
	}, nil
}

func (l *safeLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

func (l *safeLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger.Printf(format, args...)
}

func shortThreadID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return fmt.Sprintf("%s...", id[:8])
}
