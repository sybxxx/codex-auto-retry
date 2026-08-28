//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The Desktop app passes the bundled codex_app MCP definition to its app
// server as a -c override. A shared server does not receive Desktop's
// command-line construction, so it must reproduce the same effective entry.
// The file is deliberately read-only; the plugin never edits the bundled
// marketplace or the user's Codex config.
func loadCodexAppMCPOverride() (string, string, error) {
	path, found, err := findCodexAppMCPConfig()
	if err != nil {
		return "", "", err
	}
	if !found {
		// Older Codex builds may not ship the Desktop MCP definition. In that
		// case there is no extra layer to reproduce and the normal app-server
		// configuration remains the compatible fallback.
		return "", "", nil
	}
	override, err := codexAppMCPOverrideFromFile(path)
	if err != nil {
		return "", "", fmt.Errorf("parse %s: %w", path, err)
	}
	if override == "" {
		return "", "", nil
	}
	digest := sha256.Sum256([]byte(override))
	return override, fmt.Sprintf("%x", digest[:]), nil
}

func findCodexAppMCPConfig() (string, bool, error) {
	home, _ := os.UserHomeDir()
	resources := []string{
		strings.TrimSpace(os.Getenv("CODEX_ELECTRON_BUNDLED_PLUGINS_RESOURCES_PATH")),
		filepath.Join(home, ".codex", "electron-bundled-plugins"),
		filepath.Join(home, ".codex", ".tmp", "bundled-marketplaces", "openai-bundled"),
	}
	seen := make(map[string]struct{})
	for _, root := range resources {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		candidate := root
		if !strings.EqualFold(filepath.Base(filepath.Clean(root)), "codex-app-tools") {
			candidate = filepath.Join(root, "plugins", "openai-bundled", "plugins", "codex-app-tools")
		}
		candidate = filepath.Join(candidate, "desktop-mcp.json")
		key := strings.ToLower(filepath.Clean(candidate))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, true, nil
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", false, statErr
		}
	}

	// The installed Codex cache includes a versioned directory. Only inspect
	// that one known directory level instead of recursively scanning CODEX_HOME.
	cacheRoot := filepath.Join(home, ".codex", "plugins", "cache", "openai-bundled", "codex-app-tools")
	entries, err := os.ReadDir(cacheRoot)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	type candidateInfo struct {
		path string
		when int64
	}
	candidates := make([]candidateInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(cacheRoot, entry.Name(), "desktop-mcp.json")
		info, statErr := os.Stat(candidate)
		if statErr != nil || info.IsDir() {
			continue
		}
		candidates = append(candidates, candidateInfo{path: candidate, when: info.ModTime().UnixNano()})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].when == candidates[j].when {
			return candidates[i].path > candidates[j].path
		}
		return candidates[i].when > candidates[j].when
	})
	if len(candidates) > 0 {
		return candidates[0].path, true, nil
	}
	return "", false, nil
}

func codexAppMCPOverrideFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var document struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return "", err
	}
	raw, ok := document.MCPServers["codex_app"]
	if !ok {
		return "", nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", fmt.Errorf("codex_app must be an object: %w", err)
	}
	if len(object) == 0 {
		return "", errors.New("codex_app definition is empty")
	}
	// `type` belongs to the plugin JSON format. The app-server's effective
	// config infers stdio from `command`; forwarding this compatibility field
	// is what caused older shared servers to report "invalid transport".
	// Keep the remaining Desktop fields (approval policy, environment names,
	// and timeouts) so the shared server has the same MCP behavior.
	delete(object, "type")
	if command, present := object["command"]; present {
		var value string
		if err := json.Unmarshal(command, &value); err != nil || strings.TrimSpace(value) == "" {
			return "", errors.New("codex_app command is empty or invalid")
		}
	} else if endpoint, present := object["url"]; !present || string(endpoint) == "null" {
		return "", errors.New("codex_app has neither command nor url")
	}
	if cwd, present := object["cwd"]; present {
		var value string
		if err := json.Unmarshal(cwd, &value); err != nil {
			return "", fmt.Errorf("codex_app cwd is invalid: %w", err)
		}
		if value != "" && !filepath.IsAbs(value) {
			absolute := filepath.Join(filepath.Dir(path), value)
			encoded, marshalErr := json.Marshal(filepath.Clean(absolute))
			if marshalErr != nil {
				return "", marshalErr
			}
			object["cwd"] = encoded
		}
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	value, err := jsonToTOMLInline(encoded)
	if err != nil {
		return "", err
	}
	return "mcp_servers.codex_app=" + value, nil
}

func jsonToTOMLInline(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("multiple JSON values")
		}
		return "", err
	}
	return tomlInlineValue(value)
}

func tomlInlineValue(value any) (string, error) {
	switch value := value.(type) {
	case string:
		return strconv.Quote(value), nil
	case bool:
		return strconv.FormatBool(value), nil
	case json.Number:
		if value.String() == "" {
			return "", errors.New("empty JSON number")
		}
		return value.String(), nil
	case []any:
		values := make([]string, 0, len(value))
		for _, item := range value {
			encoded, err := tomlInlineValue(item)
			if err != nil {
				return "", err
			}
			values = append(values, encoded)
		}
		return "[" + strings.Join(values, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			encoded, err := tomlInlineValue(value[key])
			if err != nil {
				return "", err
			}
			parts = append(parts, strconv.Quote(key)+"="+encoded)
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	case nil:
		return "", errors.New("null is not valid TOML")
	default:
		return "", fmt.Errorf("unsupported JSON value %T", value)
	}
}
