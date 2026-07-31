package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	resumeSettingsSearchBlockBytes = 64 * 1024
	resumeSettingsPrefixBytes      = 4096
	maxResumeSettingsLineBytes     = 8 * 1024 * 1024
	maxResumeWorkspaceRoots        = 128
	maxResumeSettingStringBytes    = 32 * 1024
)

var (
	errResumeSettingsUnavailable = errors.New("resume settings unavailable")
)

type ResumeSettings struct {
	CWD                   string          `json:"cwd"`
	RuntimeWorkspaceRoots []string        `json:"runtime_workspace_roots"`
	ApprovalPolicy        json.RawMessage `json:"approval_policy"`
	ApprovalsReviewer     string          `json:"approvals_reviewer,omitempty"`
	Model                 string          `json:"model"`
	ModelProvider         string          `json:"model_provider,omitempty"`
	ServiceTier           string          `json:"service_tier,omitempty"`
	Personality           string          `json:"personality,omitempty"`
	Permissions           string          `json:"permissions"`
	Effort                string          `json:"effort"`
	Summary               string          `json:"summary,omitempty"`
}

type persistedThreadSettingsEvent struct {
	Type    string `json:"type"`
	Payload struct {
		Type           string `json:"type"`
		ThreadSettings struct {
			Model             string          `json:"model"`
			ModelProviderID   string          `json:"model_provider_id"`
			ServiceTier       string          `json:"service_tier"`
			ApprovalPolicy    json.RawMessage `json:"approval_policy"`
			ApprovalsReviewer string          `json:"approvals_reviewer"`
			PermissionProfile struct {
				Type string `json:"type"`
			} `json:"permission_profile"`
			ActivePermissionProfile *struct {
				ID string `json:"id"`
			} `json:"active_permission_profile"`
			CWD              string `json:"cwd"`
			ReasoningEffort  string `json:"reasoning_effort"`
			ReasoningSummary string `json:"reasoning_summary"`
			Personality      string `json:"personality"`
		} `json:"thread_settings"`
	} `json:"payload"`
}

type persistedTurnContext struct {
	Type    string `json:"type"`
	Payload struct {
		CWD               string          `json:"cwd"`
		WorkspaceRoots    []string        `json:"workspace_roots"`
		ApprovalPolicy    json.RawMessage `json:"approval_policy"`
		ApprovalsReviewer string          `json:"approvals_reviewer"`
		SandboxPolicy     struct {
			Type string `json:"type"`
		} `json:"sandbox_policy"`
		PermissionProfile struct {
			Type string `json:"type"`
		} `json:"permission_profile"`
		Model       string `json:"model"`
		Personality string `json:"personality"`
		Effort      string `json:"effort"`
		Summary     string `json:"summary"`
	} `json:"payload"`
}

func loadLatestResumeSettings(path string) (ResumeSettings, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !strings.EqualFold(filepath.Ext(path), ".jsonl") {
		return ResumeSettings{}, errResumeSettingsUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return ResumeSettings{}, fmt.Errorf("%w: rollout open", errResumeSettingsUnavailable)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return ResumeSettings{}, fmt.Errorf("%w: rollout stat", errResumeSettingsUnavailable)
	}
	contextLine, found, err := findLatestMatchingLine(file, info.Size(), hasTurnContextType)
	if err != nil {
		return ResumeSettings{}, fmt.Errorf("%w: %v", errResumeSettingsUnavailable, err)
	}
	if !found {
		return ResumeSettings{}, fmt.Errorf("%w: turn context missing", errResumeSettingsUnavailable)
	}
	settings, err := parseResumeSettings(contextLine)
	if err != nil {
		return ResumeSettings{}, fmt.Errorf("%w: %v", errResumeSettingsUnavailable, err)
	}
	settingsLine, settingsFound, err := findLatestMatchingLine(file, info.Size(), hasThreadSettingsAppliedType)
	if err != nil {
		return ResumeSettings{}, fmt.Errorf("%w: %v", errResumeSettingsUnavailable, err)
	}
	if settingsFound {
		if err := mergeAppliedThreadSettings(&settings, settingsLine); err != nil {
			return ResumeSettings{}, fmt.Errorf("%w: %v", errResumeSettingsUnavailable, err)
		}
	}
	if err := validateResumeSettings(settings); err != nil {
		return ResumeSettings{}, fmt.Errorf("%w: %v", errResumeSettingsUnavailable, err)
	}
	return settings, nil
}

func findThreadResumeSettings(codexHome, threadID string) (ResumeSettings, error) {
	codexHome = filepath.Clean(strings.TrimSpace(codexHome))
	threadID = strings.ToLower(strings.TrimSpace(threadID))
	if !filepath.IsAbs(codexHome) || !threadIDPattern.MatchString(threadID+".jsonl") {
		return ResumeSettings{}, errResumeSettingsUnavailable
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
		return ResumeSettings{}, fmt.Errorf("%w: parent rollout missing", errResumeSettingsUnavailable)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].updated > candidates[j].updated })
	return loadLatestResumeSettings(candidates[0].path)
}

func findLatestMatchingLine(file *os.File, size int64, matches func([]byte) bool) ([]byte, bool, error) {
	lineEnd := size
	buffer := make([]byte, resumeSettingsSearchBlockBytes)
	for position := size; position > 0; {
		readSize := int64(len(buffer))
		if position < readSize {
			readSize = position
		}
		position -= readSize
		chunk := buffer[:readSize]
		if _, err := file.ReadAt(chunk, position); err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		for index := len(chunk) - 1; index >= 0; index-- {
			if chunk[index] != '\n' {
				continue
			}
			lineStart := position + int64(index) + 1
			if line, found, err := inspectMatchingLine(file, lineStart, lineEnd, matches); found || err != nil {
				return line, found, err
			}
			lineEnd = position + int64(index)
		}
	}
	return inspectMatchingLine(file, 0, lineEnd, matches)
}

func inspectMatchingLine(file *os.File, start, end int64, matches func([]byte) bool) ([]byte, bool, error) {
	if end <= start {
		return nil, false, nil
	}
	length := end - start
	prefixLength := length
	if prefixLength > resumeSettingsPrefixBytes {
		prefixLength = resumeSettingsPrefixBytes
	}
	prefix := make([]byte, prefixLength)
	if _, err := file.ReadAt(prefix, start); err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if !matches(prefix) {
		return nil, false, nil
	}
	if length > maxResumeSettingsLineBytes {
		return nil, false, errors.New("settings record exceeds size limit")
	}
	line := make([]byte, length)
	if _, err := file.ReadAt(line, start); err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return line, true, nil
}

func hasTurnContextType(prefix []byte) bool {
	typeName, _ := topLevelStringField(prefix, "type")
	return typeName == "turn_context"
}

func hasThreadSettingsAppliedType(prefix []byte) bool {
	typeName, _ := topLevelStringField(prefix, "type")
	if typeName != "event_msg" {
		return false
	}
	return bytes.Contains(prefix, []byte(`"type":"thread_settings_applied"`)) ||
		bytes.Contains(prefix, []byte(`"type": "thread_settings_applied"`))
}

func topLevelStringField(prefix []byte, wanted string) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(prefix))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", false
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return "", false
		}
		name, ok := key.(string)
		if !ok {
			return "", false
		}
		if name == wanted {
			value, err := decoder.Token()
			text, ok := value.(string)
			return text, err == nil && ok
		}
		var ignored json.RawMessage
		if decoder.Decode(&ignored) != nil {
			return "", false
		}
	}
	return "", false
}

func parseResumeSettings(line []byte) (ResumeSettings, error) {
	var context persistedTurnContext
	if err := json.Unmarshal(line, &context); err != nil || context.Type != "turn_context" {
		return ResumeSettings{}, errors.New("invalid turn context")
	}
	permissions, err := resumePermissionProfile(
		context.Payload.SandboxPolicy.Type,
		context.Payload.PermissionProfile.Type,
	)
	if err != nil {
		return ResumeSettings{}, err
	}
	settings := ResumeSettings{
		CWD:                   context.Payload.CWD,
		RuntimeWorkspaceRoots: append([]string{}, context.Payload.WorkspaceRoots...),
		ApprovalPolicy:        append(json.RawMessage(nil), context.Payload.ApprovalPolicy...),
		ApprovalsReviewer:     context.Payload.ApprovalsReviewer,
		Model:                 context.Payload.Model,
		Personality:           context.Payload.Personality,
		Permissions:           permissions,
		Effort:                context.Payload.Effort,
		Summary:               context.Payload.Summary,
	}
	if err := validateResumeSettings(settings); err != nil {
		return ResumeSettings{}, err
	}
	return settings, nil
}

func mergeAppliedThreadSettings(settings *ResumeSettings, line []byte) error {
	var event persistedThreadSettingsEvent
	if err := json.Unmarshal(line, &event); err != nil || event.Type != "event_msg" ||
		event.Payload.Type != "thread_settings_applied" {
		return errors.New("invalid thread settings record")
	}
	applied := event.Payload.ThreadSettings
	if applied.CWD != "" {
		settings.CWD = applied.CWD
	}
	if applied.Model != "" {
		settings.Model = applied.Model
	}
	settings.ModelProvider = applied.ModelProviderID
	settings.ServiceTier = applied.ServiceTier
	if len(applied.ApprovalPolicy) > 0 {
		settings.ApprovalPolicy = append(json.RawMessage(nil), applied.ApprovalPolicy...)
	}
	if applied.ApprovalsReviewer != "" {
		settings.ApprovalsReviewer = applied.ApprovalsReviewer
	}
	if applied.ActivePermissionProfile != nil && applied.ActivePermissionProfile.ID != "" {
		settings.Permissions = applied.ActivePermissionProfile.ID
	}
	if applied.ReasoningEffort != "" {
		settings.Effort = applied.ReasoningEffort
	}
	if applied.ReasoningSummary != "" {
		settings.Summary = applied.ReasoningSummary
	}
	if applied.Personality != "" {
		settings.Personality = applied.Personality
	}
	return nil
}

func resumePermissionProfile(sandboxType, profileType string) (string, error) {
	switch sandboxType {
	case "danger-full-access":
		if profileType != "" && profileType != "disabled" {
			return "", errors.New("inconsistent danger-full-access permission profile")
		}
		return ":danger-full-access", nil
	case "read-only":
		if profileType != "" && profileType != "managed" {
			return "", errors.New("inconsistent read-only permission profile")
		}
		return ":read-only", nil
	case "workspace-write":
		if profileType != "" && profileType != "managed" {
			return "", errors.New("inconsistent workspace permission profile")
		}
		return ":workspace", nil
	default:
		return "", errors.New("unsupported sandbox policy")
	}
}

func validateResumeSettings(settings ResumeSettings) error {
	if !validAbsoluteSettingPath(settings.CWD) {
		return errors.New("invalid working directory")
	}
	if len(settings.RuntimeWorkspaceRoots) > maxResumeWorkspaceRoots {
		return errors.New("invalid workspace roots")
	}
	for _, root := range settings.RuntimeWorkspaceRoots {
		if !validAbsoluteSettingPath(root) {
			return errors.New("invalid workspace root")
		}
	}
	if !validBoundedSetting(settings.Model) || !validBoundedSetting(settings.Effort) {
		return errors.New("invalid model settings")
	}
	if settings.ModelProvider != "" && !validBoundedSetting(settings.ModelProvider) {
		return errors.New("invalid model provider")
	}
	if settings.ServiceTier != "" && !validBoundedSetting(settings.ServiceTier) {
		return errors.New("invalid service tier")
	}
	if !validBoundedSetting(settings.Permissions) {
		return errors.New("invalid permission profile")
	}
	if !validApprovalPolicy(settings.ApprovalPolicy) {
		return errors.New("invalid approval policy")
	}
	if settings.ApprovalsReviewer != "" && settings.ApprovalsReviewer != "user" &&
		settings.ApprovalsReviewer != "auto_review" && settings.ApprovalsReviewer != "guardian_subagent" {
		return errors.New("invalid approvals reviewer")
	}
	if settings.Personality != "" && settings.Personality != "none" &&
		settings.Personality != "friendly" && settings.Personality != "pragmatic" {
		return errors.New("invalid personality")
	}
	if settings.Summary != "" && settings.Summary != "none" && settings.Summary != "auto" &&
		settings.Summary != "concise" && settings.Summary != "detailed" {
		return errors.New("invalid reasoning summary")
	}
	return nil
}

func validAbsoluteSettingPath(value string) bool {
	return filepath.IsAbs(value) && validBoundedSetting(value)
}

func validBoundedSetting(value string) bool {
	if value == "" || len(value) > maxResumeSettingStringBytes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validApprovalPolicy(raw json.RawMessage) bool {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return name == "untrusted" || name == "on-request" || name == "never"
	}
	var policy struct {
		Granular map[string]bool `json:"granular"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&policy) != nil || policy.Granular == nil {
		return false
	}
	allowed := map[string]struct{}{
		"mcp_elicitations": {}, "request_permissions": {}, "rules": {},
		"sandbox_approval": {}, "skill_approval": {},
	}
	for key := range policy.Granular {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	_, hasMCP := policy.Granular["mcp_elicitations"]
	_, hasRules := policy.Granular["rules"]
	_, hasSandbox := policy.Granular["sandbox_approval"]
	return hasMCP && hasRules && hasSandbox
}
