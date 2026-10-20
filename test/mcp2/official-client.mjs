// Independent interoperability test: no AlertBridge protocol helper is imported.
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import assert from 'node:assert/strict';
import { Client, StreamableHTTPClientTransport } from './tmp/inspector/node_modules/@modelcontextprotocol/client/dist/index.mjs';

const packageInfo = JSON.parse(await readFile(new URL("./tmp/inspector/node_modules/@modelcontextprotocol/client/package.json", import.meta.url), "utf8"));
assert.equal(packageInfo.version, "2.3.1");
const state = JSON.parse(await readFile(new URL('./tmp/sandbox/state.json', import.meta.url), 'utf8'));
const evidence = [];
const transport = new StreamableHTTPClientTransport(new URL('http://127.0.0.1:18083/mcp'), {
  requestInit: { headers: { Authorization: `Bearer ${state.mcp_token}` } },
  fetch: async (url, init) => {
    const req = JSON.parse(init.body);
    const response = await fetch(url, init);
    const body = await response.clone().json();
    // Only synthetic query data; never persist authorization headers or callback secrets.
    evidence.push({ method: req.method, request: req, status: response.status, response: body });
    return response;
  },
});
const client = new Client({ name: 'alertbridge-independent-mcp2-check', version: '1' }, {
  capabilities: {}, versionNegotiation: { mode: { pin: '2026-07-28' } },
});
try {
  await client.connect(transport);
  const discovery = await client.discover();
  assert.deepEqual(discovery.supportedVersions, ['2026-07-28']);
  assert.ok(discovery.capabilities.tools);
  assert.ok(evidence.find(e => e.method === "server/discover").response.result.capabilities.events, "Events must be advertised on the wire; the core SDK strips unknown draft capabilities");
  const tools = await client.listTools();
  assert.deepEqual(tools.tools.map(t => t.name).sort(), ['get_alert', 'list_active_incidents', 'list_alerts']);
  const latest = await client.callTool({ name: 'list_alerts', arguments: { order: 'desc', limit: 10 } });
  assert.equal(latest.isError, false);
  assert.ok(latest.structuredContent.alerts.length > 0, 'emit a sandbox event first');
  const detail = await client.callTool({ name: 'get_alert', arguments: { id: latest.structuredContent.alerts[0].id } });
  assert.equal(detail.isError, false);
  const active = await client.callTool({ name: 'list_active_incidents', arguments: {} });
  assert.equal(active.isError, false);
  assert.ok(evidence.every(e => e.method !== 'initialize'), 'modern connection must not initialize');
  assert.ok(evidence.every(e => e.response.result?.resultType === 'complete'));
  await mkdir(new URL('./tmp/evidence/', import.meta.url), { recursive: true });
  await writeFile(new URL('./tmp/evidence/official-client.json', import.meta.url), JSON.stringify(evidence, null, 2));
  console.log('Official @modelcontextprotocol/client 2.3.1: pinned MCP 2026-07-28 discovery and all three query tools passed; no initialize or version rewriting.');
} finally {
  await client.close();
}
