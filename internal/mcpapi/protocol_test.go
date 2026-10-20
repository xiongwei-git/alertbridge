package mcpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func modernTestRequest(t *testing.T, r *http.Request) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	params, _ := req["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
		req["params"] = params
	}
	params["_meta"] = map[string]any{metaVersion: EventsProtocolVersion, metaCapabilities: map[string]any{}, "io.modelcontextprotocol/clientInfo": map[string]string{"name": "alertbridge-test", "version": "1"}}
	body, _ = json.Marshal(req)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", EventsProtocolVersion)
	method, _ := req["method"].(string)
	r.Header.Set("Mcp-Method", method)
	if name, ok := params["name"].(string); ok && req["method"] == "tools/call" {
		r.Header.Set("Mcp-Name", name)
	}
}

func TestMCP2WireContract(t *testing.T) {
	f := newEventFixture(t)
	cases := []struct {
		name         string
		change       func(*http.Request, map[string]any)
		status, code int
	}{
		{"valid query", func(*http.Request, map[string]any) {}, 200, 0},
		{"missing metadata", func(_ *http.Request, p map[string]any) { delete(p, "_meta") }, 400, -32602},
		{"missing capabilities", func(_ *http.Request, p map[string]any) { delete(p["_meta"].(map[string]any), metaCapabilities) }, 400, -32602},
		{"null capabilities", func(_ *http.Request, p map[string]any) { p["_meta"].(map[string]any)[metaCapabilities] = nil }, 400, -32602},
		{"missing version header", func(r *http.Request, _ map[string]any) { r.Header.Del("MCP-Protocol-Version") }, 400, -32020},
		{"mismatched version", func(r *http.Request, _ map[string]any) { r.Header.Set("MCP-Protocol-Version", "2025-11-25") }, 400, -32020},
		{"unsupported version", func(r *http.Request, p map[string]any) {
			r.Header.Set("MCP-Protocol-Version", "2099-01-01")
			p["_meta"].(map[string]any)[metaVersion] = "2099-01-01"
		}, 400, -32022},
		{"missing method header", func(r *http.Request, _ map[string]any) { r.Header.Del("Mcp-Method") }, 400, -32020},
		{"method mismatch", func(r *http.Request, _ map[string]any) { r.Header.Set("Mcp-Method", "tools/call") }, 400, -32020},
		{"duplicate method header", func(r *http.Request, _ map[string]any) { r.Header.Add("Mcp-Method", "tools/list") }, 400, -32020},
		{"bad origin", func(r *http.Request, _ map[string]any) { r.Header.Set("Origin", "https://untrusted.example") }, 403, 0},
		{"wrong content type", func(r *http.Request, _ map[string]any) { r.Header.Set("Content-Type", "text/plain") }, 415, -32600},
		{"incomplete accept", func(r *http.Request, _ map[string]any) { r.Header.Set("Accept", "application/json") }, 406, -32600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
			modernTestRequest(t, req)
			req.Header.Set("Authorization", "Bearer "+f.token)
			body, _ := io.ReadAll(req.Body)
			var envelope map[string]any
			json.Unmarshal(body, &envelope)
			tc.change(req, envelope["params"].(map[string]any))
			body, _ = json.Marshal(envelope)
			req.Body = io.NopCloser(bytes.NewReader(body))
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if tc.code != 0 {
				var v struct {
					Error eventRPCError `json:"error"`
				}
				if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Error.Code != tc.code {
					t.Fatal(w.Body.String())
				}
			}
		})
	}
	for _, nameHeader := range []string{"list_alerts", "=?base64?bGlzdF9hbGVydHM=?="} {
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"tool","method":"tools/call","params":{"name":"list_alerts","arguments":{}}}`))
		modernTestRequest(t, req)
		req.Header.Set("Authorization", "Bearer "+f.token)
		req.Header.Set("Mcp-Name", nameHeader)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"resultType":"complete"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, method := range []string{"initialize", "unknown/method"} {
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"`+method+`","params":{}}`))
		modernTestRequest(t, req)
		req.Header.Set("Authorization", "Bearer "+f.token)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":-32601`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
