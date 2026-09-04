package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const (
	maxLogBytes = 5 * 1024 * 1024
	maxLogFiles = 4 // active log plus three bounded backups
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
	// Remove any backup beyond the supported retention window left by an older
	// build before opening the active log.
	_ = os.Remove(path + "." + strconv.Itoa(maxLogFiles))
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		rotateLogFiles(path)
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

func rotateLogFiles(path string) {
	_ = os.Remove(path + "." + strconv.Itoa(maxLogFiles))
	for index := maxLogFiles - 2; index >= 1; index-- {
		source := path + "." + strconv.Itoa(index)
		destination := path + "." + strconv.Itoa(index+1)
		_ = os.Remove(destination)
		_ = os.Rename(source, destination)
	}
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
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
