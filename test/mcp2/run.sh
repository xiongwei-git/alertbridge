#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$root"
if [ ! -x test/mcp2/tmp/venv/bin/python ]; then python3 -m venv test/mcp2/tmp/venv; fi
if ! test/mcp2/tmp/venv/bin/python -c 'import jsonschema' >/dev/null 2>&1; then
  test/mcp2/tmp/venv/bin/pip install jsonschema==4.25.1
fi
if [ ! -f test/mcp2/tmp/inspector/node_modules/@modelcontextprotocol/client/dist/index.mjs ]; then
  npm install --prefix test/mcp2/tmp/inspector --no-audit --no-fund @modelcontextprotocol/client@2.3.1
fi
python3 scripts/mcp-events-sandbox.py up
python3 scripts/mcp-events-sandbox.py emit
node test/mcp2/official-client.mjs
test/mcp2/tmp/venv/bin/python test/mcp2/validate.py
go test ./internal/mcpapi -run 'TestEvents|TestModernProtocol|TestMCP2WireContract'
