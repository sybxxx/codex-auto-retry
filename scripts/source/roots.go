package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type sessionRoot struct {
	Sessions         string
	ArchivedSessions string
	CodexHome        string
}

func discoverSessionRoots(cfg Config) []sessionRoot {
	seen := make(map[string]sessionRoot)
	add := func(path string) {
		path = expandPath(path)
		base := strings.ToLower(filepath.Base(path))
		home := path
		if base == "sessions" || base == "archived_sessions" {
			home = filepath.Dir(path)
		}
		sessions := filepath.Join(home, "sessions")
		archived := filepath.Join(home, "archived_sessions")
		sessionsInfo, sessionsErr := os.Stat(sessions)
		archivedInfo, archivedErr := os.Stat(archived)
		if (sessionsErr != nil || !sessionsInfo.IsDir()) && (archivedErr != nil || !archivedInfo.IsDir()) {
			return
		}
		home = filepath.Clean(home)
		clean := strings.ToLower(home)
		root := sessionRoot{CodexHome: home}
		if sessionsErr == nil && sessionsInfo.IsDir() {
			root.Sessions = sessions
		}
		if archivedErr == nil && archivedInfo.IsDir() {
			root.ArchivedSessions = archived
		}
		seen[clean] = root
	}

	if cfg.IncludeDefaultHome {
		if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
			add(codexHome)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if cfg.IncludeDefaultHome {
			add(filepath.Join(home, ".codex"))
		}
		if cfg.IncludeCockpitHomes {
			pattern := filepath.Join(home, ".antigravity_cockpit", "instances", "codex", "*", "sessions")
			matches, _ := filepath.Glob(pattern)
			for _, match := range matches {
				add(match)
			}
		}
	}
	for _, root := range cfg.SessionRoots {
		add(root)
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]sessionRoot, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result
}
