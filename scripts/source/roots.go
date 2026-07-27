package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type sessionRoot struct {
	Sessions  string
	CodexHome string
}

func discoverSessionRoots(cfg Config) []sessionRoot {
	seen := make(map[string]sessionRoot)
	add := func(path string) {
		path = expandPath(path)
		if filepath.Base(path) != "sessions" {
			path = filepath.Join(path, "sessions")
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return
		}
		clean := strings.ToLower(filepath.Clean(path))
		seen[clean] = sessionRoot{Sessions: filepath.Clean(path), CodexHome: filepath.Dir(filepath.Clean(path))}
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
