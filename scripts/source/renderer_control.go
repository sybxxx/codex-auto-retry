package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const createNoWindow = 0x08000000

var (
	errRendererTargetNotFound = errors.New("Codex renderer target not found")
	errRendererProtocol       = errors.New("Codex renderer protocol failed")
	errRendererEvaluation     = errors.New("Codex renderer evaluation failed")
)

type debugPortDiscoverer interface {
	Discover(context.Context) ([]int, error)
}

type configuredPortDiscoverer struct {
	port int
}

func (d configuredPortDiscoverer) Discover(context.Context) ([]int, error) {
	return []int{d.port}, nil
}

type powerShellPortDiscoverer struct {
	configuredExecutable string
}

const debugPortDiscoveryScript = `$ErrorActionPreference = 'Stop'
Get-CimInstance Win32_Process -ErrorAction Stop | ForEach-Object {
    $line = $_.CommandLine
    if ($line -and $line -match '(?:^|\s)--remote-debugging-port=(\d+)(?:\s|$)') {
        [Console]::Out.WriteLine($Matches[1])
    }
}`

func (d powerShellPortDiscoverer) Discover(ctx context.Context) ([]int, error) {
	powerShell, err := resolvePowerShellExecutable(d.configuredExecutable)
	if err != nil {
		return nil, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, powerShell,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = strings.NewReader(powerShellScriptInput(debugPortDiscoveryScript))
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	ports := parseDebugPorts(stdout.String())
	if len(ports) == 0 {
		return nil, errRendererTargetNotFound
	}
	return ports, nil
}

type rendererController struct {
	config     Config
	discoverer debugPortDiscoverer
	httpClient *http.Client
	dialer     *websocket.Dialer

	targetMu   sync.Mutex
	cachedPort int
}

type cdpTarget struct {
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type cdpRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type cdpResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type runtimeEvaluateResponse struct {
	Result struct {
		Type        string          `json:"type"`
		Description string          `json:"description,omitempty"`
		Value       json.RawMessage `json:"value,omitempty"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text string `json:"text"`
	} `json:"exceptionDetails,omitempty"`
}

type rendererProbeResult struct {
	OK             bool `json:"ok"`
	RouteUnchanged bool `json:"route_unchanged"`
	RPCFound       bool `json:"rpc_found"`
	SnapshotRead   bool `json:"snapshot_read"`
	LoadedListRead bool `json:"loaded_list_read"`
}

func newRendererController(config Config) *rendererController {
	var discoverer debugPortDiscoverer = powerShellPortDiscoverer{
		configuredExecutable: config.PowerShellExecutable,
	}
	if config.RendererDebugPort > 0 {
		discoverer = configuredPortDiscoverer{port: config.RendererDebugPort}
	}
	return &rendererController{
		config:     config,
		discoverer: discoverer,
		httpClient: &http.Client{Timeout: 3 * time.Second},
		dialer:     websocket.DefaultDialer,
	}
}

func (c *rendererController) Dispatch(
	ctx context.Context,
	threadID string,
	prompt string,
	settings ResumeSettings,
	failedAt time.Time,
	originTurnStartedAt time.Time,
	recoveryEventID string,
	parentNotified bool,
	goalLimitRestart bool,
	failureClass FailureClass,
) (DispatchResult, error) {
	payload, err := json.Marshal(struct {
		ThreadID                string         `json:"thread_id"`
		Prompt                  string         `json:"prompt"`
		Settings                ResumeSettings `json:"settings"`
		FailedAtUnixMS          int64          `json:"failed_at_unix_ms"`
		OriginTurnStartedUnixMS int64          `json:"origin_turn_started_at_unix_ms,omitempty"`
		RecoveryEventID         string         `json:"recovery_event_id,omitempty"`
		ParentNotified          bool           `json:"parent_notified,omitempty"`
		GoalLimitRestart        bool           `json:"goal_limit_restart,omitempty"`
		FailureClass            FailureClass   `json:"failure_class"`
	}{
		ThreadID: threadID, Prompt: prompt, Settings: settings, FailedAtUnixMS: failedAt.UnixMilli(),
		OriginTurnStartedUnixMS: timeToUnixMilli(originTurnStartedAt),
		RecoveryEventID:         recoveryEventID, ParentNotified: parentNotified, GoalLimitRestart: goalLimitRestart,
		FailureClass: failureClass,
	})
	if err != nil {
		return DispatchResult{}, errRendererEvaluation
	}
	expression := strings.Replace(rendererDispatchExpression, "__PAYLOAD__", string(payload), 1)
	value, err := c.evaluate(ctx, expression)
	if err != nil {
		return DispatchResult{}, err
	}
	var result DispatchResult
	if err := json.Unmarshal(value, &result); err != nil {
		return DispatchResult{}, errControllerInvalidResult
	}
	return result, nil
}

func (c *rendererController) BlockGoal(ctx context.Context, threadID string) (DispatchResult, error) {
	if !threadIDPattern.MatchString(threadID + ".jsonl") {
		return DispatchResult{}, errRendererEvaluation
	}
	payload, err := json.Marshal(struct {
		ThreadID string `json:"thread_id"`
	}{ThreadID: threadID})
	if err != nil {
		return DispatchResult{}, errRendererEvaluation
	}
	expression := strings.Replace(rendererGoalBlockExpression, "__PAYLOAD__", string(payload), 1)
	value, err := c.evaluate(ctx, expression)
	if err != nil {
		return DispatchResult{}, err
	}
	var result DispatchResult
	if err := json.Unmarshal(value, &result); err != nil {
		return DispatchResult{}, errControllerInvalidResult
	}
	return result, nil
}

func timeToUnixMilli(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}

func (c *rendererController) Probe(ctx context.Context) (rendererProbeResult, error) {
	value, err := c.evaluate(ctx, rendererProbeExpression)
	if err != nil {
		return rendererProbeResult{}, err
	}
	var result rendererProbeResult
	if err := json.Unmarshal(value, &result); err != nil {
		return rendererProbeResult{}, errRendererEvaluation
	}
	if !result.OK || !result.RouteUnchanged || !result.RPCFound || !result.SnapshotRead || !result.LoadedListRead {
		return rendererProbeResult{}, errRendererEvaluation
	}
	return result, nil
}

func (c *rendererController) evaluate(ctx context.Context, expression string) (json.RawMessage, error) {
	target, err := c.findTarget(ctx)
	if err != nil {
		return nil, err
	}
	connection, _, err := c.dialer.DialContext(ctx, target.WebSocketDebuggerURL, nil)
	if err != nil {
		c.clearCachedPort()
		return nil, errRendererProtocol
	}
	defer connection.Close()
	deadline := time.Now().Add(25 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetReadDeadline(deadline)
	_ = connection.SetWriteDeadline(deadline)
	if _, err := c.call(connection, 1, "Runtime.enable", nil); err != nil {
		return nil, err
	}
	params := map[string]any{
		"expression":    expression,
		"awaitPromise":  true,
		"returnByValue": true,
	}
	raw, err := c.call(connection, 2, "Runtime.evaluate", params)
	if err != nil {
		return nil, err
	}
	var response runtimeEvaluateResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, errRendererProtocol
	}
	if response.ExceptionDetails != nil || len(response.Result.Value) == 0 {
		return nil, errRendererEvaluation
	}
	return response.Result.Value, nil
}

func (c *rendererController) call(connection *websocket.Conn, id int, method string, params any) (json.RawMessage, error) {
	if err := connection.WriteJSON(cdpRequest{ID: id, Method: method, Params: params}); err != nil {
		return nil, errRendererProtocol
	}
	for {
		_, data, err := connection.ReadMessage()
		if err != nil {
			return nil, errRendererProtocol
		}
		var response cdpResponse
		if json.Unmarshal(data, &response) != nil || response.ID == 0 {
			continue
		}
		if response.ID != id {
			continue
		}
		if response.Error != nil {
			return nil, errRendererProtocol
		}
		return response.Result, nil
	}
}

func (c *rendererController) findTarget(ctx context.Context) (cdpTarget, error) {
	c.targetMu.Lock()
	defer c.targetMu.Unlock()
	if c.cachedPort > 0 {
		if target, err := c.targetForPort(ctx, c.cachedPort); err == nil {
			return target, nil
		}
		c.cachedPort = 0
	}
	ports, err := c.discoverer.Discover(ctx)
	if err != nil {
		return cdpTarget{}, errRendererTargetNotFound
	}
	for _, port := range ports {
		target, err := c.targetForPort(ctx, port)
		if err != nil {
			continue
		}
		c.cachedPort = port
		return target, nil
	}
	return cdpTarget{}, errRendererTargetNotFound
}

func (c *rendererController) clearCachedPort() {
	c.targetMu.Lock()
	c.cachedPort = 0
	c.targetMu.Unlock()
}

func (c *rendererController) targetForPort(ctx context.Context, port int) (cdpTarget, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return cdpTarget{}, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return cdpTarget{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return cdpTarget{}, errRendererTargetNotFound
	}
	var targets []cdpTarget
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1024*1024))
	if err := decoder.Decode(&targets); err != nil {
		return cdpTarget{}, errRendererProtocol
	}
	for _, target := range targets {
		if target.Type != "page" || target.Title != "Codex" || target.URL != "app://-/index.html" {
			continue
		}
		if !validLoopbackWebSocket(target.WebSocketDebuggerURL, port) {
			continue
		}
		return target, nil
	}
	return cdpTarget{}, errRendererTargetNotFound
}

func validLoopbackWebSocket(rawURL string, expectedPort int) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "ws" || parsed.User != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port == expectedPort
}

func parseDebugPorts(output string) []int {
	pattern := regexp.MustCompile(`\b\d{1,5}\b`)
	seen := make(map[int]struct{})
	ports := make([]int, 0)
	for _, match := range pattern.FindAllString(output, -1) {
		port, err := strconv.Atoi(match)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		if _, exists := seen[port]; exists {
			continue
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

const rendererModuleBootstrap = `
    const indexScript = document.querySelector('script[type="module"]')?.src;
    if (!indexScript) return null;
    const indexSource = await (await fetch(indexScript)).text();
    const asset = indexSource.match(/app-initial-[A-Za-z0-9_-]+\.js/)?.[0];
    if (!asset) return null;
    const module = await import(new URL(asset, indexScript).href);
    const candidates = [];
    for (const value of Object.values(module)) {
        if (typeof value !== "function" || value.length !== 2) continue;
        let source;
        try { source = String(value); } catch { continue; }
        if (/return [A-Za-z0-9_$]+\.sendRequest\(e,t\)/.test(source)) candidates.push(value);
    }
    return candidates.length === 1 ? candidates[0] : null;`

const rendererDispatchExpression = `(async () => {
    const payload = __PAYLOAD__;
	let parentNotified = Boolean(payload.parent_notified);
	const retryLater = reason => ({outcome: "retry_later", action: "", reason, parent_notified: parentNotified});
	const hold = reason => ({outcome: "not_applicable", action: "", reason, parent_notified: parentNotified});
    const unixSeconds = value => {
        const numeric = Number(value);
        if (!Number.isFinite(numeric) || numeric <= 0) return 0;
        return numeric > 100000000000 ? numeric / 1000 : numeric;
    };
    const blockedByFailure = goal => {
		if (payload.goal_limit_restart && goal?.status === "blocked") return true;
        const failedAt = unixSeconds(payload.failed_at_unix_ms);
        const updatedAt = unixSeconds(goal?.updatedAt);
        return failedAt > 0 && updatedAt >= failedAt - 2 && updatedAt <= failedAt + 5;
    };
	const heldConversationAllowed = goal => {
		const startedAt = unixSeconds(payload.origin_turn_started_at_unix_ms);
		const updatedAt = unixSeconds(goal?.updatedAt);
		const status = goal?.status ?? null;
		if (startedAt <= 0 || updatedAt <= 0 || updatedAt > startedAt) return false;
		if (status === "paused") return true;
		return status === "blocked" && !blockedByFailure(goal);
	};
	const goalRevision = goal => String(goal?.status ?? "") + "|" + String(goal?.updatedAt ?? "");
    const goalHoldReason = goal => {
        if (!goal) return "";
        const status = goal?.status ?? null;
        if (!status) return "goal_status_unsupported";
        if (status === "active") return "";
        if (status === "blocked") {
            return blockedByFailure(goal) ? "" : "goal_blocked_before_failure";
        }
        if (status === "paused") return "goal_paused";
        if (status === "completed") return "goal_completed";
        if (status === "usageLimited") return "goal_usage_limited";
        if (status === "budgetLimited") return "goal_budget_limited";
        return "goal_status_unsupported";
    };
    const classifyError = error => {
        const message = String(error?.message ?? error).toLowerCase();
        if (message.includes("active") || message.includes("already running")) return "thread_active";
        if (message.includes("not found") || message.includes("unknown thread")) return "thread_not_found";
        if (message.includes("model provider") || message.includes("configuration")) return "thread_config_unavailable";
        if (message.includes("not initialized") || message.includes("not ready") || message.includes("no appservermanager")) return "codex_app_not_ready";
        return "app_server_request_failed";
    };
    const emptyInputUnsupported = error => {
        const message = String(error?.message ?? error).toLowerCase();
        return message.includes("input must not be empty") ||
            message.includes("input array must not be empty") ||
            message.includes("at least one input") ||
            (message.includes("input") && message.includes("minitems"));
    };
	const startConversation = async (request, goalHeld) => {
		try {
			await request("turn/start", {threadId: payload.thread_id, input: []});
			return {
				outcome: "dispatched",
				action: "conversation_continue",
				reason: goalHeld ? "silent_turn_started_with_goal_held" : "silent_turn_started_in_background"
			};
		} catch (error) {
			if (!emptyInputUnsupported(error)) throw error;
			await request("turn/start", {
				threadId: payload.thread_id,
				input: [{type: "text", text: payload.prompt, text_elements: []}]
			});
			return {
				outcome: "dispatched",
				action: "conversation_continue",
				reason: goalHeld ? "fallback_turn_started_with_goal_held" : "fallback_turn_started_in_background"
			};
		}
	};
	const startSubagent = async request => {
		await request("turn/start", {threadId: payload.thread_id, input: []});
		return {outcome: "dispatched", action: "subagent_continue", reason: "subagent_resumed_in_background", parent_notified: parentNotified};
	};
    try {
        const rpc = await (async () => {` + rendererModuleBootstrap + `})();
        if (!rpc) return retryLater("renderer_rpc_not_found");
        const request = (method, params) => rpc("send-cli-request-for-host", {
            hostId: "local", method, params, source: "codex_auto_retry"
        });
        const initial = await request("thread/read", {threadId: payload.thread_id, includeTurns: false});
        const initialStatus = initial?.thread?.status?.type;
        if (initialStatus === "active") return retryLater("thread_active");
        if (!initialStatus) return retryLater("thread_state_unavailable");
		const isSubagent = Boolean(initial?.thread?.parentThreadId || initial?.thread?.source?.subAgent);
		const parentThreadId = initial?.thread?.parentThreadId ??
			initial?.thread?.source?.subAgent?.thread_spawn?.parent_thread_id ?? null;
		const loadedResponse = await request("thread/loaded/list", {});
        const isLoaded = Array.isArray(loadedResponse?.data) &&
            loadedResponse.data.includes(payload.thread_id);
		let initialGoal = null;
		let continuingWhileGoalHeld = false;
		let initialGoalRevision = "";
		if (!isSubagent) {
			const goalResponse = await request("thread/goal/get", {threadId: payload.thread_id});
			initialGoal = goalResponse?.goal ?? null;
			const initialHoldReason = goalHoldReason(initialGoal);
			continuingWhileGoalHeld = heldConversationAllowed(initialGoal);
			if (initialHoldReason && !continuingWhileGoalHeld) return hold(initialHoldReason);
			initialGoalRevision = goalRevision(initialGoal);
		}
        await rpc("hydrate-background-threads", {
            hostId: "local", threadIds: [payload.thread_id], includeTurns: false
        });
        const resumeConfig = {model_reasoning_effort: payload.settings.effort};
        if (payload.settings.summary) resumeConfig.model_reasoning_summary = payload.settings.summary;
        const resumeParams = {
            threadId: payload.thread_id,
            excludeTurns: true,
            cwd: payload.settings.cwd,
            runtimeWorkspaceRoots: payload.settings.runtime_workspace_roots,
            model: payload.settings.model,
            approvalPolicy: payload.settings.approval_policy,
            permissions: payload.settings.permissions,
            config: resumeConfig
        };
        if (payload.settings.approvals_reviewer) {
            resumeParams.approvalsReviewer = payload.settings.approvals_reviewer;
        }
        if (payload.settings.model_provider) resumeParams.modelProvider = payload.settings.model_provider;
        if (payload.settings.service_tier) resumeParams.serviceTier = payload.settings.service_tier;
        if (payload.settings.personality) resumeParams.personality = payload.settings.personality;
        let resumedStatus = initialStatus;
		if (!isLoaded) {
            const resumed = await request("thread/resume", resumeParams);
			resumedStatus = resumed?.thread?.status?.type;
		}
		if (isSubagent) {
			if (payload.failure_class !== "empty_response") return hold("subagent_non_empty_failure");
			if (!parentThreadId) return retryLater("subagent_parent_unavailable");
			if (!parentNotified) {
				if (!/^car-[0-9a-f]{24}$/.test(String(payload.recovery_event_id ?? ""))) {
					return retryLater("subagent_recovery_event_unavailable");
				}
				const notice = "codex-auto-retry:subagent-empty-response-recovery:v1:" + JSON.stringify({
					parent_thread_id: parentThreadId,
					child_thread_id: payload.thread_id,
					recovery_event_id: payload.recovery_event_id,
					action: "resume_existing_child",
					spawn_replacement: false,
					instruction: "The watchdog is resuming the exact existing child. Do not resume or spawn any child for this recovery event."
				});
				await request("thread/inject_items", {
					threadId: parentThreadId,
					items: [{
						type: "message",
						id: "msg_codex_auto_retry_" + payload.recovery_event_id.slice(4),
						role: "developer",
						content: [{type: "input_text", text: notice}]
					}]
				});
				parentNotified = true;
			}
			const latestChild = await request("thread/read", {threadId: payload.thread_id, includeTurns: false});
			if (latestChild?.thread?.status?.type === "active" || resumedStatus === "active") {
				return retryLater("thread_active");
			}
			return await startSubagent(request);
		}
		const latestGoalResponse = await request("thread/goal/get", {threadId: payload.thread_id});
        const latestGoal = latestGoalResponse?.goal ?? null;
        const latestGoalStatus = latestGoal?.status ?? null;
        if (Boolean(initialGoal) !== Boolean(latestGoal)) return hold("goal_status_changed");
		if (continuingWhileGoalHeld) {
			if (!heldConversationAllowed(latestGoal) || goalRevision(latestGoal) !== initialGoalRevision) {
				return hold("goal_status_changed");
			}
			if (resumedStatus === "active") return retryLater("thread_active");
			return await startConversation(request, true);
		}
        const latestHoldReason = goalHoldReason(latestGoal);
        if (latestHoldReason) return hold(latestHoldReason);
        const goalCanContinue = latestGoalStatus === "active" || latestGoalStatus === "blocked";
        if (goalCanContinue) {
            if (latestGoalStatus === "blocked") {
                await request("thread/goal/set", {threadId: payload.thread_id, status: "active"});
            }
            return {outcome: "dispatched", action: "goal_resume", reason: "goal_resumed_in_background"};
        }
        if (resumedStatus === "active") return retryLater("thread_active");
		return await startConversation(request, false);
    } catch (error) {
        return retryLater(classifyError(error));
    }
})()`

const rendererGoalBlockExpression = `(async () => {
	const payload = __PAYLOAD__;
	const retryLater = reason => ({outcome: "retry_later", action: "", reason});
	const stopped = reason => ({outcome: "dispatched", action: "goal_block", reason});
	try {
		const rpc = await (async () => {` + rendererModuleBootstrap + `})();
		if (!rpc) return retryLater("renderer_rpc_not_found");
		const request = (method, params) => rpc("send-cli-request-for-host", {
			hostId: "local", method, params, source: "codex_auto_retry"
		});
		const goalResponse = await request("thread/goal/get", {threadId: payload.thread_id});
		const status = goalResponse?.goal?.status ?? null;
		if (status === "active") {
			await request("thread/goal/set", {threadId: payload.thread_id, status: "blocked"});
			return stopped("goal_blocked_after_empty_response_limit");
		}
		if (status === "blocked") return stopped("goal_already_blocked");
		if (status === "paused") return stopped("goal_already_paused");
		if (status === "completed" || status === "complete") return stopped("goal_already_completed");
		if (status === "usageLimited") return stopped("goal_already_usage_limited");
		if (status === "budgetLimited") return stopped("goal_already_budget_limited");
		return retryLater("goal_state_unavailable");
	} catch {
		return retryLater("app_server_request_failed");
	}
})()`

const rendererProbeExpression = `(async () => {
    const routeBefore = location.href;
    try {
        const rpc = await (async () => {` + rendererModuleBootstrap + `})();
        if (!rpc) return {ok: false, route_unchanged: location.href === routeBefore, rpc_found: false, snapshot_read: false, loaded_list_read: false};
        const snapshot = await rpc("collect-app-state-snapshot", {reason: "codex-auto-retry-read-only-probe"});
        const loaded = await rpc("send-cli-request-for-host", {
            hostId: "local", method: "thread/loaded/list", params: {}, source: "codex_auto_retry_probe"
        });
        return {
            ok: true,
            route_unchanged: location.href === routeBefore,
            rpc_found: true,
            snapshot_read: snapshot != null && typeof snapshot === "object",
            loaded_list_read: Array.isArray(loaded?.data)
        };
    } catch {
        return {ok: false, route_unchanged: location.href === routeBefore, rpc_found: false, snapshot_read: false, loaded_list_read: false};
    }
})()`
