#!/usr/bin/env node
/*
 * Build-time patch for @mmmbuto/zai-codex-bridge@0.4.9.
 *
 * Problem: when Z.AI returns HTTP 429 (code 1305 "temporarily overloaded") the
 * bridge surfaces it to Codex as a `response.failed` event, which Codex treats as
 * a stream disconnect. After stream_max_retries reconnects, `codex exec` exits and
 * the container workspace (all progress) is discarded.
 *
 * Fix: retry the upstream Z.AI request on 429 / 5xx / transport errors with
 * exponential backoff, bounded by ZAI_RETRY_MAX_MS, so a rate-limit storm becomes
 * a pause rather than a fatal disconnect. Because Z.AI rejects with 429 *before*
 * any streaming starts, retrying the POST is safe (no partial output to dedup).
 * Codex keeps the same run alive, so its on-disk workspace state survives.
 *
 * Each bridge retry window is kept under Codex's stream_idle_timeout_ms, and
 * Codex's own stream_max_retries stacks on top, so long storms are ridden out
 * across many windows without ever restarting the run.
 *
 * Pinned to @0.4.9; throws (failing the image build) if the source drifts.
 */
const fs = require('fs');

const target = process.argv[2] ||
  '/usr/local/lib/node_modules/@mmmbuto/zai-codex-bridge/src/server.js';
let src = fs.readFileSync(target, 'utf8');

// ---- Edit 1: insert the retry helper right after makeUpstreamRequest() ends ----
const anchorEnd =
  "  const response = await fetch(url, {\n" +
  "    method: 'POST',\n" +
  "    headers: upstreamHeaders,\n" +
  "    body: JSON.stringify(body)\n" +
  "  });\n\n" +
  "  return response;\n" +
  "}\n";
if (src.indexOf(anchorEnd) === -1) {
  throw new Error('zai-bridge-patch: anchor for makeUpstreamRequest() end not found (version drift?)');
}

const helper = [
  '',
  'function __sleep(ms) { return new Promise(function (r) { setTimeout(r, ms); }); }',
  '',
  '// Added by zai-bridge-patch: retry Z.AI 429/5xx/transport failures instead of',
  '// surfacing them to Codex as a stream disconnect, so a rate-limit storm becomes',
  '// a pause and the Codex run (with its workspace) survives.',
  'async function makeUpstreamRequestWithRetry(path, body, headers, onWait) {',
  "  var maxMs  = parseInt(process.env.ZAI_RETRY_MAX_MS  || '', 10) || 2400000;",
  "  var baseMs = parseInt(process.env.ZAI_RETRY_BASE_MS || '', 10) || 2000;",
  "  var capMs  = parseInt(process.env.ZAI_RETRY_CAP_MS  || '', 10) || 20000;",
  '  var deadline = Date.now() + maxMs;',
  '  var attempt = 0;',
  '  for (;;) {',
  '    attempt++;',
  '    var response = null, err = null;',
  '    try { response = await makeUpstreamRequest(path, body, headers); }',
  '    catch (e) { err = e; }',
  '    var retryable = !!err || response.status === 429 || response.status >= 500;',
  '    if (!retryable) return response;',
  '    if (Date.now() >= deadline) {',
  "      log('error', '[retry] giving up after ' + attempt + ' attempts (deadline reached)',",
  '        { status: response && response.status, err: err && err.message });',
  '      if (response) return response;',
  '      throw err;',
  '    }',
  '    var delay = Math.min(capMs, baseMs * Math.pow(2, Math.min(attempt - 1, 5)))',
  '      + Math.floor(Math.random() * 1000);',
  "    log('warn', '[retry] upstream ' + (err ? 'error ' + err.message : 'status ' + response.status)",
  "      + '; attempt ' + attempt + ', waiting ' + delay + 'ms (elapsed ' + (Date.now() - (deadline - maxMs)) + 'ms)');",
  '    if (typeof onWait === "function") { try { onWait(attempt, delay); } catch (_) {} }',
  '    await __sleep(delay);',
  '  }',
  '}',
  ''
].join('\n');

src = src.replace(anchorEnd, anchorEnd + helper);

// ---- Edit 2: route the /responses upstream call through the retry wrapper ----
const callAnchor =
  "    const upstreamResponse = await makeUpstreamRequest(\n" +
  "      '/chat/completions',\n" +
  "      upstreamBody,\n" +
  "      req.headers\n" +
  "    );";
if (src.indexOf(callAnchor) === -1) {
  throw new Error('zai-bridge-patch: anchor for upstream call in handlePostRequest not found (version drift?)');
}
const callReplacement =
  "    const upstreamResponse = await makeUpstreamRequestWithRetry(\n" +
  "      '/chat/completions',\n" +
  "      upstreamBody,\n" +
  "      req.headers\n" +
  "    );";
src = src.replace(callAnchor, callReplacement);

if (src.indexOf('makeUpstreamRequestWithRetry') === -1) {
  throw new Error('zai-bridge-patch: post-condition failed, wrapper not present');
}

fs.writeFileSync(target, src);
console.log('zai-bridge-patch: applied retry wrapper to ' + target);
