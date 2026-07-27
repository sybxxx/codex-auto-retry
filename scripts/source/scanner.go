package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var threadIDPattern = regexp.MustCompile(`(?i)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

type scannedEvent struct {
	ThreadID    string
	Root        sessionRoot
	RolloutPath string
	Event       RelevantEvent
	Mirrored    bool
}

func scanSessions(roots []sessionRoot, state *RuntimeState, now time.Time, baseline bool) ([]scannedEvent, error) {
	events := make([]scannedEvent, 0)
	for _, root := range roots {
		err := filepath.WalkDir(root.Sessions, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
				return nil
			}
			threadID := threadIDFromPath(path)
			if threadID == "" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			key := strings.ToLower(filepath.Clean(path))
			cursor, known := state.Files[key]
			if baseline || !known && state.Initialized == false {
				state.Files[key] = FileCursor{Offset: info.Size(), LastSeen: now}
				return nil
			}
			if !known {
				cursor = FileCursor{}
			}
			mirrored := !known && threadKnownInFiles(state.Files, threadID)
			if info.Size() < cursor.Offset {
				state.Files[key] = FileCursor{Offset: info.Size(), LastSeen: now}
				return nil
			}
			if info.Size() == cursor.Offset {
				cursor.LastSeen = now
				state.Files[key] = cursor
				return nil
			}
			newEvents, nextOffset, err := readAppendedEvents(path, cursor.Offset, threadID, root, mirrored)
			if err != nil {
				return nil
			}
			events = append(events, newEvents...)
			state.Files[key] = FileCursor{Offset: nextOffset, LastSeen: now}
			return nil
		})
		if err != nil {
			return events, fmt.Errorf("scan %s: %w", root.Sessions, err)
		}
	}
	return events, nil
}

func readAppendedEvents(path string, offset int64, threadID string, root sessionRoot, mirrored bool) ([]scannedEvent, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	current := offset
	events := make([]scannedEvent, 0)
	for {
		lineStart := current
		line, readErr := reader.ReadBytes('\n')
		current += int64(len(line))
		if readErr == io.EOF && len(line) > 0 && !bytes.HasSuffix(line, []byte{'\n'}) {
			current = lineStart
			break
		}
		if len(line) > 0 {
			if event, ok := parseRelevantEvent(line); ok {
				events = append(events, scannedEvent{
					ThreadID:    threadID,
					Root:        root,
					RolloutPath: filepath.Clean(path),
					Event:       event,
					Mirrored:    mirrored,
				})
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return events, current, readErr
		}
	}
	return events, current, nil
}

func threadKnownInFiles(files map[string]FileCursor, threadID string) bool {
	needle := strings.ToLower(threadID) + ".jsonl"
	for path := range files {
		if strings.HasSuffix(path, needle) {
			return true
		}
	}
	return false
}

func threadIDFromPath(path string) string {
	match := threadIDPattern.FindStringSubmatch(filepath.Base(path))
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(match[1])
}
