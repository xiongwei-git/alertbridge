#!/usr/bin/env python3
"""Independent wire/schema checks against the pinned upstream MCP 2.0 schema."""
import copy
import hashlib
import json
from pathlib import Path
import urllib.error
import urllib.request
from jsonschema import Draft202012Validator

HERE = Path(__file__).resolve().parent
schema_bytes = (HERE / "fixtures/schema-2026-07-28.json").read_bytes()
source = json.loads((HERE / "fixtures/source.json").read_text())
assert hashlib.sha256(schema_bytes).hexdigest() == source["sha256"]
schema = json.loads(schema_bytes)
Draft202012Validator.check_schema(schema)
state = json.loads((HERE / "tmp/sandbox/state.json").read_text())
transport = urllib.request.build_opener(urllib.request.ProxyHandler({}))
evidence = []


def validate(definition, value):
    Draft202012Validator({"$ref": "#/$defs/" + definition, "$defs": schema["$defs"]}).validate(value)


def rpc(method, params=None, *, change=None):
    params = copy.deepcopy(params or {})
    params["_meta"] = {"io.modelcontextprotocol/protocolVersion": "2026-07-28",
                       "io.modelcontextprotocol/clientCapabilities": {},
                       "io.modelcontextprotocol/clientInfo": {"name": "independent-schema-check", "version": "1"}}
    body = {"jsonrpc": "2.0", "id": len(evidence) + 1, "method": method, "params": params}
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
               "Authorization": "Bearer " + state["mcp_token"], "MCP-Protocol-Version": "2026-07-28", "Mcp-Method": method}
    if method == "tools/call":
        headers["Mcp-Name"] = params["name"]
    if not change and method in {"server/discover", "tools/list", "tools/call"}:
        validate({"server/discover": "DiscoverRequest", "tools/list": "ListToolsRequest", "tools/call": "CallToolRequest"}[method], body)
    if change:
        change(body, headers)
    request = urllib.request.Request("http://127.0.0.1:18083/mcp", json.dumps(body).encode(), headers)
    try:
        response = transport.open(request, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        status, reply = response.status, json.load(response)
    validate("JSONRPCResponse", reply)
    if "result" in reply:
        assert reply["result"]["resultType"] == "complete"
        assert reply["result"]["_meta"]["io.modelcontextprotocol/serverInfo"]["name"] == "alertbridge"
    evidence.append({"method": method, "status": status, "response": reply})
    return status, reply


for method, definition, result_definition, params in [
    ("server/discover", "DiscoverRequest", "DiscoverResult", {}),
    ("tools/list", "ListToolsRequest", "ListToolsResult", {}),
    ("tools/call", "CallToolRequest", "CallToolResult", {"name": "list_alerts", "arguments": {"order": "desc", "limit": 10}}),
    ("tools/call", "CallToolRequest", "CallToolResult", {"name": "list_active_incidents", "arguments": {}}),
]:
    status, reply = rpc(method, params)
    assert status == 200 and "error" not in reply, (method, status, reply)
    validate(result_definition, reply["result"])

status, events = rpc("events/list")
assert status == 200
assert events["result"]["events"][0]["name"] == "alert.created"
for event in events["result"]["events"]:
    Draft202012Validator.check_schema(event["inputSchema"])
    Draft202012Validator.check_schema(event["payloadSchema"])

mutations = [
    (lambda b, h: b["params"].pop("_meta"), -32602),
    (lambda b, h: b["params"]["_meta"].pop("io.modelcontextprotocol/clientCapabilities"), -32602),
    (lambda b, h: h.pop("MCP-Protocol-Version"), -32020),
    (lambda b, h: h.update({"Mcp-Method": "tools/call"}), -32020),
    (lambda b, h: (h.update({"MCP-Protocol-Version": "2099-01-01"}), b["params"]["_meta"].update({"io.modelcontextprotocol/protocolVersion": "2099-01-01"})), -32022),
]
for change, code in mutations:
    status, reply = rpc("tools/list", change=change)
    assert status == 400 and reply["error"]["code"] == code, (status, reply)
status, reply = rpc("tools/call", {"name": "list_alerts", "arguments": {}}, change=lambda b, h: h.pop("Mcp-Name"))
assert status == 400 and reply["error"]["code"] == -32020
status, reply = rpc("initialize")
assert status == 404 and reply["error"]["code"] == -32601
status, reply = rpc("events/subscribe", {"name": "alert.created", "arguments": {}, "delivery": {"mode": "webhook", "url": "http://127.0.0.1/callback", "secret": "not-a-secret"}})
assert reply["error"]["code"] == -32602

out = HERE / "tmp/evidence"
out.mkdir(parents=True, exist_ok=True)
(out / "schema-check.json").write_text(json.dumps({"source": source, "checks": evidence}, indent=2) + "\n")
print(f"Pinned official MCP 2026-07-28 JSON Schema and {len(evidence)} independent wire checks passed.")
