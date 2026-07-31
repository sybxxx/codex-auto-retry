[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

function Find-CodexBinary {
    foreach ($root in @(
        (Join-Path $env:LOCALAPPDATA 'OpenAI\Codex\bin'),
        (Join-Path $env:APPDATA 'npm\node_modules\@openai\codex')
    )) {
        if (-not (Test-Path -LiteralPath $root -PathType Container)) { continue }
        $candidate = Get-ChildItem -LiteralPath $root -Recurse -Filter 'codex.exe' -File -ErrorAction SilentlyContinue |
            Sort-Object LastWriteTime -Descending |
            Select-Object -First 1
        if ($candidate) { return $candidate.FullName }
    }
    throw 'Codex CLI was not found for the shared app-server test.'
}

$node = Get-Command node.exe -ErrorAction Stop
$oldCodexExecutable = $env:CODEX_SHARED_SERVER_TEST_CODEX_EXE
$env:CODEX_SHARED_SERVER_TEST_CODEX_EXE = Find-CodexBinary

$nodeScript = @'
const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');
const net = require('net');
const { spawn } = require('child_process');

const tempBase = path.resolve(os.tmpdir());
const home = fs.mkdtempSync(path.join(tempBase, 'codex-shared-server-'));
const upstreamBodies = [];
let provider;
let appServer;
let stderr = '';

function delay(milliseconds) {
  return new Promise(resolve => setTimeout(resolve, milliseconds));
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function stream(events) {
  return events.map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join('');
}

function responseCreated(id) {
  return { type: 'response.created', response: { id } };
}

function responseCompleted(id) {
  return {
    type: 'response.completed',
    response: {
      id,
      usage: {
        input_tokens: 0,
        input_tokens_details: null,
        output_tokens: 0,
        output_tokens_details: null,
        total_tokens: 0,
      },
    },
  };
}

function assistantMessage(id, text) {
  return {
    type: 'response.output_item.done',
    item: {
      type: 'message',
      role: 'assistant',
      id,
      content: [{ type: 'output_text', text }],
    },
  };
}

async function freePort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const port = server.address().port;
  await new Promise(resolve => server.close(resolve));
  return port;
}

class RpcClient {
  constructor(name, socket) {
    this.name = name;
    this.socket = socket;
    this.nextId = 1;
    this.pending = new Map();
    this.messages = [];
    this.waiters = [];
    socket.addEventListener('message', event => this.handle(JSON.parse(String(event.data))));
  }

  handle(message) {
    this.messages.push(message);
    if (message.method && message.id !== undefined) {
      this.socket.send(JSON.stringify({
        jsonrpc: '2.0',
        id: message.id,
        error: { code: -32601, message: 'Interactive request unavailable in isolated test' },
      }));
    }
    if (message.id !== undefined && this.pending.has(message.id)) {
      const entry = this.pending.get(message.id);
      this.pending.delete(message.id);
      clearTimeout(entry.timer);
      if (message.error) entry.reject(new Error(message.error.message || JSON.stringify(message.error)));
      else entry.resolve(message.result);
    }
    for (let index = this.waiters.length - 1; index >= 0; index -= 1) {
      if (!this.waiters[index].predicate(message)) continue;
      const waiter = this.waiters.splice(index, 1)[0];
      clearTimeout(waiter.timer);
      waiter.resolve(message);
    }
  }

  call(method, params = {}) {
    const id = this.nextId++;
    const result = new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`${this.name} timed out waiting for ${method}`));
      }, 30000);
      this.pending.set(id, { resolve, reject, timer });
    });
    this.socket.send(JSON.stringify({ jsonrpc: '2.0', id, method, params }));
    return result;
  }

  notify(method, params = {}) {
    this.socket.send(JSON.stringify({ jsonrpc: '2.0', method, params }));
  }

  waitFor(predicate, timeoutMilliseconds = 30000) {
    const existing = this.messages.find(predicate);
    if (existing) return Promise.resolve(existing);
    return new Promise((resolve, reject) => {
      const waiter = { predicate, resolve, timer: null };
      waiter.timer = setTimeout(() => {
        const index = this.waiters.indexOf(waiter);
        if (index >= 0) this.waiters.splice(index, 1);
        reject(new Error(`${this.name} timed out waiting for a notification`));
      }, timeoutMilliseconds);
      this.waiters.push(waiter);
    });
  }

  close() {
    this.socket.close();
  }
}

async function connectClient(endpoint, name) {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    try {
      const socket = new WebSocket(endpoint);
      await new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error('open timeout')), 1500);
        socket.addEventListener('open', () => { clearTimeout(timer); resolve(); }, { once: true });
        socket.addEventListener('error', () => { clearTimeout(timer); reject(new Error('open failed')); }, { once: true });
      });
      const client = new RpcClient(name, socket);
      await client.call('initialize', {
        clientInfo: { name, version: '0.0.0' },
        capabilities: { experimentalApi: true },
      });
      client.notify('initialized', {});
      return client;
    } catch {
      await delay(150);
    }
  }
  throw new Error(`Could not connect to ${endpoint}. App-server stderr: ${stderr.slice(-2000)}`);
}

function userInputCount(body) {
  if (!Array.isArray(body?.input)) return 0;
  return body.input.filter(item => item && item.role === 'user').length;
}

(async () => {
  let desktop;
  let watchdog;
  try {
    provider = http.createServer((request, response) => {
      const chunks = [];
      request.on('data', chunk => chunks.push(chunk));
      request.on('end', () => {
        let body = null;
        try { body = JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch {}
        upstreamBodies.push(body);
        const requestNumber = upstreamBodies.length;
        const responseId = `response_${requestNumber}`;
        const events = requestNumber === 1
          ? [responseCreated(responseId), responseCompleted(responseId)]
          : [
              responseCreated(responseId),
              assistantMessage('recovered_message', 'RECOVERED_THROUGH_SHARED_SERVER'),
              responseCompleted(responseId),
            ];
        response.writeHead(200, {
          'Content-Type': 'text/event-stream',
          'Cache-Control': 'no-cache',
          Connection: 'close',
        });
        response.end(stream(events));
      });
    });
    await new Promise((resolve, reject) => {
      provider.once('error', reject);
      provider.listen(0, '127.0.0.1', resolve);
    });
    const providerPort = provider.address().port;
    const wsPort = await freePort();
    const endpoint = `ws://127.0.0.1:${wsPort}`;
    const config = `model = 'integration-test-model'\nmodel_provider = 'integration_test'\n\n[model_providers.integration_test]\nname = 'Integration Test'\nbase_url = 'http://127.0.0.1:${providerPort}/v1'\nenv_key = 'CODEX_INTEGRATION_TEST_KEY'\nwire_api = 'responses'\nrequires_openai_auth = false\nrequest_max_retries = 0\nstream_max_retries = 0\nstream_idle_timeout_ms = 1000\n`;
    fs.writeFileSync(path.join(home, 'config.toml'), config, 'utf8');

    appServer = spawn(
      process.env.CODEX_SHARED_SERVER_TEST_CODEX_EXE,
      ['-c', 'features.code_mode_host=true', 'app-server', '--analytics-default-enabled', '--listen', endpoint],
      {
        windowsHide: true,
        env: {
          ...process.env,
          CODEX_HOME: home,
          CODEX_APP_SERVER_WS_URL: endpoint,
          CODEX_INTEGRATION_TEST_KEY: 'isolated-diagnostic-only',
        },
        stdio: ['ignore', 'ignore', 'pipe'],
      },
    );
    appServer.stderr.on('data', data => { stderr += data.toString('utf8'); });

    desktop = await connectClient(endpoint, 'codex_desktop_simulator');
    watchdog = await connectClient(endpoint, 'codex_auto_retry_watchdog');
    const thread = await desktop.call('thread/start', {
      cwd: home,
      model: 'integration-test-model',
      modelProvider: 'integration_test',
      approvalPolicy: 'never',
      permissions: ':danger-full-access',
    });
    const threadId = thread.thread.id;
    const original = await desktop.call('turn/start', {
      threadId,
      input: [{ type: 'text', text: 'ORIGINAL_SHARED_SERVER_TEST_PROMPT', text_elements: [] }],
    });
    await desktop.waitFor(message =>
      message.method === 'turn/completed' && message.params?.turn?.id === original.turn.id,
    );

    const loaded = await watchdog.call('thread/loaded/list', {});
    assert(Array.isArray(loaded.data) && loaded.data.includes(threadId), 'The watchdog client could not see the desktop task.');
    const before = await watchdog.call('thread/read', { threadId, includeTurns: false });
    assert(before.thread?.id === threadId, 'The watchdog client read a different task.');
    const continuation = await watchdog.call('turn/start', { threadId, input: [] });
    const desktopStarted = await desktop.waitFor(message =>
      message.method === 'turn/started' && message.params?.turn?.id === continuation.turn.id,
    );
    const desktopCompleted = await desktop.waitFor(message =>
      message.method === 'turn/completed' && message.params?.turn?.id === continuation.turn.id,
    );
    const read = await desktop.call('thread/read', { threadId, includeTurns: true });
    const turns = read.thread?.turns || [];
    const originalBody = JSON.stringify(upstreamBodies[0] || {});
    const continuationBody = JSON.stringify(upstreamBodies[1] || {});

    assert(upstreamBodies.length === 2, 'The watchdog recovery did not create exactly one additional provider request.');
    assert(originalBody.includes('ORIGINAL_SHARED_SERVER_TEST_PROMPT'), 'The original provider request lost the user prompt.');
    assert(continuationBody.includes('ORIGINAL_SHARED_SERVER_TEST_PROMPT'), 'The shared-server continuation lost prior context.');
    assert(userInputCount(upstreamBodies[0]) === userInputCount(upstreamBodies[1]), 'The recovery added a provider-visible user item.');
    assert(desktopStarted.params?.threadId === threadId, 'The desktop client observed a retry for a different task.');
    assert(desktopCompleted.params?.turn?.status === 'completed', 'The desktop client did not observe retry completion.');
    assert(!(turns[1]?.items || []).some(item => item.type === 'userMessage'), 'The recovery added a visible user message.');
    assert((turns[1]?.items || []).find(item => item.type === 'agentMessage')?.text === 'RECOVERED_THROUGH_SHARED_SERVER', 'The recovered reply was not stored in the desktop task.');

    console.log(JSON.stringify({
      Status: 'passed',
      SharedWebSocketServer: true,
      DesktopClientSawStarted: true,
      DesktopClientSawCompleted: true,
      ProviderRequests: upstreamBodies.length,
      VisibleUserMessageAdded: false,
      RealProviderUsed: false,
    }));
  } finally {
    if (desktop) desktop.close();
    if (watchdog) watchdog.close();
    if (appServer && appServer.exitCode === null) {
      appServer.kill();
      await Promise.race([new Promise(resolve => appServer.once('exit', resolve)), delay(3000)]);
      if (appServer.exitCode === null) appServer.kill('SIGKILL');
    }
    if (provider) await new Promise(resolve => provider.close(resolve));
    await delay(100);
    const resolvedHome = path.resolve(home);
    const safePrefix = tempBase.endsWith(path.sep) ? tempBase : tempBase + path.sep;
    if (!resolvedHome.startsWith(safePrefix) || !path.basename(resolvedHome).startsWith('codex-shared-server-')) {
      throw new Error(`Refusing to clean unsafe test path: ${resolvedHome}`);
    }
    fs.rmSync(resolvedHome, { recursive: true, force: true });
  }
})().catch(error => {
  console.error(error.stack || String(error));
  process.exitCode = 1;
});
'@

try {
    & $node.Source -e $nodeScript
    if ($LASTEXITCODE -ne 0) { throw 'Shared app-server smoke test failed.' }
}
finally {
    if ($null -eq $oldCodexExecutable) { Remove-Item Env:CODEX_SHARED_SERVER_TEST_CODEX_EXE -ErrorAction SilentlyContinue }
    else { $env:CODEX_SHARED_SERVER_TEST_CODEX_EXE = $oldCodexExecutable }
}
