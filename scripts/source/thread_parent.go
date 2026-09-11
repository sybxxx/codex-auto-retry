package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// findThreadParentID reads only the small session metadata record needed to
// identify an internal child. It never scans message content and returns an
// empty parent for ordinary user tasks.
func findThreadParentID(codexHome, threadID string) (string, error) {
	codexHome = filepath.Clean(strings.TrimSpace(codexHome))
	threadID = strings.ToLower(strings.TrimSpace(threadID))
	if !filepath.IsAbs(codexHome) || !threadIDPattern.MatchString(threadID+".jsonl") {
		return "", errors.New("invalid thread identity")
	}
	type candidate struct {
		path    string
		updated int64
	}
	candidates := make([]candidate, 0, 1)
	for _, directory := range []string{"sessions", "archived_sessions"} {
		root := filepath.Join(codexHome, directory)
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || threadIDFromPath(path) != threadID {
				return nil
			}
			info, err := entry.Info()
			if err == nil {
				candidates = append(candidates, candidate{path: filepath.Clean(path), updated: info.ModTime().UnixNano()})
			}
			return nil
		})
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].updated > candidates[j].updated })
	file, err := os.Open(candidates[0].path)
	if err != nil {
		return "", nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !strings.Contains(string(line), "parent_thread_id") && !strings.Contains(string(line), "parentThreadId") {
			continue
		}
		var record struct {
			ParentThreadID string `json:"parent_thread_id"`
			Payload        struct {
				ParentThreadID  string `json:"parent_thread_id"`
				ParentThreadID2 string `json:"parentThreadId"`
			} `json:"payload"`
			Source struct {
				SubAgent struct {
					ThreadSpawn struct {
						ParentThreadID string `json:"parent_thread_id"`
					} `json:"thread_spawn"`
				} `json:"subAgent"`
			} `json:"source"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		for _, value := range []string{
			record.ParentThreadID, record.Payload.ParentThreadID, record.Payload.ParentThreadID2,
			record.Source.SubAgent.ThreadSpawn.ParentThreadID,
		} {
			value = strings.ToLower(strings.TrimSpace(value))
			if threadIDPattern.MatchString(value + ".jsonl") {
				return value, nil
			}
		}
		// The first matching metadata line is authoritative. Do not fall back
		// to arbitrary message content when it does not contain a valid parent.
		return "", nil
	}
	return "", scanner.Err()
}
