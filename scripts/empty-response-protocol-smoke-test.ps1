[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

function Find-CodexBinary {
    $npmRoot = Join-Path $env:APPDATA 'npm\node_modules\@openai\codex'
    if (Test-Path -LiteralPath $npmRoot) {
        $candidate = Get-ChildItem -LiteralPath $npmRoot -Recurse -Filter 'codex.exe' -File -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if ($candidate) { return $candidate.FullName }
    }
    throw 'The standalone Codex CLI binary was not found for the empty-response protocol test.'
}

$node = Get-Command node.exe -ErrorAction Stop
$oldCodexExecutable = $env:CODEX_EMPTY_RESPONSE_TEST_CODEX_EXE
$env:CODEX_EMPTY_RESPONSE_TEST_CODEX_EXE = Find-CodexBinary

$nodeScript = @'
const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');
const { spawn } = require('child_process');
const readline = require('readline');

const tempBase = path.resolve(os.tmpdir());
const home = fs.mkdtempSync(path.join(tempBase, 'codex-empty-response-protocol-'));
const upstreamBodies = [];
const pending = new Map();
const messages = [];
const waiters = [];
let nextId = 1;
let app;
let server;

function delay(milliseconds) {
  return new Promise(resolve => setTimeout(resolve, milliseconds));
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

function handleMessage(message) {
  messages.push(message);
  if (message.id !== undefined && pending.has(message.id)) {
    const entry = pending.get(message.id);
    pending.delete(message.id);
    clearTimeout(entry.timer);
    if (message.error) entry.reject(new Error(message.error.message || JSON.stringify(message.error)));
    else entry.resolve(message.result);
  }
  for (let index = waiters.length - 1; index >= 0; index -= 1) {
    if (!waiters[index].predicate(message)) continue;
    const waiter = waiters.splice(index, 1)[0];
    clearTimeout(waiter.timer);
    waiter.resolve(message);
  }
}

function send(method, params, expectsResponse = true) {
  if (!expectsResponse) {
    app.stdin.write(JSON.stringify({ method, params }) + '\n');
    return Promise.resolve(null);
  }
  const id = nextId;
  nextId += 1;
  const result = new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      pending.delete(id);
      reject(new Error(`Timed out waiting for ${method}`));
    }, 30000);
    pending.set(id, { resolve, reject, timer });
  });
  app.stdin.write(JSON.stringify({ method, id, params }) + '\n');
  return result;
}

function waitFor(predicate, timeoutMilliseconds = 30000) {
  const existing = messages.find(predicate);
  if (existing) return Promise.resolve(existing);
  return new Promise((resolve, reject) => {
    const waiter = { predicate, resolve, timer: null };
    waiter.timer = setTimeout(() => {
      const index = waiters.indexOf(waiter);
      if (index >= 0) waiters.splice(index, 1);
      reject(new Error('Timed out waiting for app-server notification'));
    }, timeoutMilliseconds);
    waiters.push(waiter);
  });
}

function userInputCount(body) {
  if (!Array.isArray(body?.input)) return 0;
  return body.input.filter(item => item && item.role === 'user').length;
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

(async () => {
  try {
    server = http.createServer((request, response) => {
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
              assistantMessage('recovered_message', 'RECOVERED_AFTER_EMPTY_RESPONSE'),
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
      server.once('error', reject);
      server.listen(0, '127.0.0.1', resolve);
    });

    const port = server.address().port;
    const config = `model = 'integration-test-model'\nmodel_provider = 'integration_test'\n\n[model_providers.integration_test]\nname = 'Integration Test'\nbase_url = 'http://127.0.0.1:${port}/v1'\nenv_key = 'CODEX_INTEGRATION_TEST_KEY'\nwire_api = 'responses'\nrequires_openai_auth = false\nrequest_max_retries = 0\nstream_max_retries = 0\nstream_idle_timeout_ms = 1000\n`;
    fs.writeFileSync(path.join(home, 'config.toml'), config, 'utf8');

    const environment = {
      ...process.env,
      CODEX_HOME: home,
      CODEX_INTEGRATION_TEST_KEY: 'isolated-diagnostic-only',
    };
    app = spawn(
      process.env.CODEX_EMPTY_RESPONSE_TEST_CODEX_EXE,
      ['app-server', '--stdio'],
      { windowsHide: true, env: environment, stdio: ['pipe', 'pipe', 'pipe'] },
    );
    readline.createInterface({ input: app.stdout }).on('line', line => {
      try { handleMessage(JSON.parse(line)); } catch {}
    });

    await send('initialize', {
      clientInfo: { name: 'codex_auto_retry_empty_response_test', version: '0.0.0' },
      capabilities: { experimentalApi: true },
    });
    await send('initialized', {}, false);
    const thread = await send('thread/start', {
      cwd: home,
      model: 'integration-test-model',
      modelProvider: 'integration_test',
      approvalPolicy: 'never',
      permissions: ':danger-full-access',
    });
    const threadId = thread.thread.id;

    const original = await send('turn/start', {
      threadId,
      input: [{ type: 'text', text: 'ORIGINAL_EMPTY_RESPONSE_TEST_PROMPT', text_elements: [] }],
    });
    const originalCompleted = await waitFor(message =>
      message.method === 'turn/completed' && message.params?.turn?.id === original.turn.id,
    );

    const continuation = await send('turn/start', { threadId, input: [] });
    const continuationCompleted = await waitFor(message =>
      message.method === 'turn/completed' && message.params?.turn?.id === continuation.turn.id,
    );
    const read = await send('thread/read', { threadId, includeTurns: true });
    const turns = read.thread?.turns || [];
    const originalBody = JSON.stringify(upstreamBodies[0] || {});
    const continuationBody = JSON.stringify(upstreamBodies[1] || {});

    assert(upstreamBodies.length === 2, 'Silent continuation did not create exactly one additional provider request.');
    assert(originalCompleted.params?.turn?.status === 'completed', 'The empty provider response was not represented as a completed Codex turn.');
    assert((turns[0]?.items || []).filter(item => item.type === 'agentMessage').length === 0, 'The empty provider response unexpectedly contained an agent message.');
    assert(continuationCompleted.params?.turn?.status === 'completed', 'The silent continuation did not complete.');
    assert(originalBody.includes('ORIGINAL_EMPTY_RESPONSE_TEST_PROMPT'), 'The original provider request lost the user prompt.');
    assert(continuationBody.includes('ORIGINAL_EMPTY_RESPONSE_TEST_PROMPT'), 'The silent continuation lost the original user prompt from context.');
    assert(userInputCount(upstreamBodies[0]) === userInputCount(upstreamBodies[1]), 'The silent continuation added a provider-visible user item.');
    assert(!(turns[1]?.items || []).some(item => item.type === 'userMessage'), 'The silent continuation added a visible user message.');
    assert((turns[1]?.items || []).find(item => item.type === 'agentMessage')?.text === 'RECOVERED_AFTER_EMPTY_RESPONSE', 'The recovered assistant reply was not stored in the same task.');

    console.log(JSON.stringify({
      Status: 'passed',
      IsolatedCodexHome: true,
      EmptySuccessReproduced: true,
      AdditionalProviderRequest: true,
      OriginalContextPreserved: true,
      VisibleUserMessageAdded: false,
      RecoveredAssistantReply: true,
      RealAccountOrProviderUsed: false,
    }));
  } finally {
    if (app && app.exitCode === null) {
      app.stdin.end();
      await Promise.race([new Promise(resolve => app.once('exit', resolve)), delay(1500)]);
      if (app.exitCode === null) app.kill();
    }
    if (server) await new Promise(resolve => server.close(resolve));
    await delay(100);
    const resolvedHome = path.resolve(home);
    const safePrefix = tempBase.endsWith(path.sep) ? tempBase : tempBase + path.sep;
    if (!resolvedHome.startsWith(safePrefix) || !path.basename(resolvedHome).startsWith('codex-empty-response-protocol-')) {
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
    if ($LASTEXITCODE -ne 0) { throw 'Empty-response protocol smoke test failed.' }
}
finally {
    $env:CODEX_EMPTY_RESPONSE_TEST_CODEX_EXE = $oldCodexExecutable
}
