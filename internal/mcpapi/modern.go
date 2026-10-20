package mcpapi

import (
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const metaVersion = "io.modelcontextprotocol/protocolVersion"
const metaCapabilities = "io.modelcontextprotocol/clientCapabilities"

var supportedVersions = []string{EventsProtocolVersion}

type modernRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func validRequestID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return true
	}
	_, err := strconv.ParseInt(string(raw), 10, 64)
	return err == nil
}
func modernError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string, data any) {
	w.WriteHeader(status)
	eventReply(w, id, nil, &eventRPCError{Code: code, Message: message, Data: data})
}
func modernOriginAllowed(r *http.Request, oauth *OAuth) bool {
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 {
		return false
	}
	origin := values[0]
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	if oauth != nil && origin == oauth.issuer {
		return true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return origin == scheme+"://"+r.Host
}
func mirroredHeader(r *http.Request, key string) (string, bool) {
	values := r.Header.Values(key)
	if len(values) != 1 {
		return "", false
	}
	value := values[0]
	if strings.HasPrefix(value, "=?base64?") && strings.HasSuffix(value, "?=") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(value, "=?base64?"), "?="))
		return string(raw), err == nil && utf8.Valid(raw)
	}
	if strings.TrimSpace(value) != value {
		return "", false
	}
	for _, c := range value {
		if c > 126 || (c < 32 && c != '\t') {
			return "", false
		}
	}
	return value, true
}
func acceptsModern(r *http.Request) bool {
	jsonOK, streamOK := false, false
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		kind, p, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || p["q"] == "0" {
			continue
		}
		jsonOK = jsonOK || kind == "application/json"
		streamOK = streamOK || kind == "text/event-stream"
	}
	return jsonOK && streamOK
}

// serveModern implements 2026-07-28 directly. No initialize handshake, protocol
// session, or legacy SDK transport is involved in processing these requests.
func serveModern(w http.ResponseWriter, r *http.Request, owner string, body []byte, events *Events, tools map[string]queryTool, oauth *OAuth) bool {
	var req modernRequest
	parseErr := json.Unmarshal(body, &req)

	w.Header().Set("Content-Type", "application/json")
	fail := func(status, code int, message string) { modernError(w, status, req.ID, code, message, nil) }
	if !modernOriginAllowed(r, oauth) {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	if parseErr != nil {
		modernError(w, 400, nil, -32700, "parse error", nil)
		return true
	}
	if req.JSONRPC != "2.0" || req.Method == "" || !validRequestID(req.ID) {
		modernError(w, 400, nil, -32600, "invalid request", nil)
		return true
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		fail(415, -32600, "content type must be application/json")
		return true
	}
	if !acceptsModern(r) {
		fail(406, -32600, "Accept must include application/json and text/event-stream")
		return true
	}
	// No core client notification needs processing over HTTP. Unknown notifications
	// are ignored, as JSON-RPC requires, and acknowledged without a response body.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return true
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(req.Params, &params) != nil || params == nil {
		fail(400, -32602, "required per-request metadata missing")
		return true
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(params["_meta"], &meta) != nil || meta == nil {
		fail(400, -32602, "required per-request metadata missing")
		return true
	}
	var version string
	var capabilities map[string]json.RawMessage
	if json.Unmarshal(meta[metaVersion], &version) != nil || version == "" || json.Unmarshal(meta[metaCapabilities], &capabilities) != nil || capabilities == nil {
		fail(400, -32602, "invalid per-request protocol metadata")
		return true
	}
	headerVersion := r.Header.Values("MCP-Protocol-Version")
	if len(headerVersion) != 1 || headerVersion[0] != version {
		fail(400, -32020, "protocol version header mismatch")
		return true
	}
	method, ok := mirroredHeader(r, "Mcp-Method")
	if !ok || method != req.Method {
		fail(400, -32020, "method header mismatch")
		return true
	}
	if version != EventsProtocolVersion {
		modernError(w, 400, req.ID, -32022, "unsupported protocol version", map[string]any{"supported": supportedVersions, "requested": version})
		return true
	}
	if req.Method == "tools/call" || req.Method == "prompts/get" || req.Method == "resources/read" || len(r.Header.Values("Mcp-Name")) > 0 {
		key := "name"
		if req.Method == "resources/read" {
			key = "uri"
		}
		var name string
		header, ok := mirroredHeader(r, "Mcp-Name")
		if json.Unmarshal(params[key], &name) != nil || !ok || header != name {
			fail(400, -32020, "name header mismatch")
			return true
		}
	}
	// Metadata has already been validated. Remove it before typed application
	// argument decoding; it must not be confused with subscription filters.
	delete(params, "_meta")
	req.Params, _ = json.Marshal(params)
	if req.Method == "server/discover" {
		caps := map[string]any{"tools": map[string]any{}}
		if events != nil {
			caps["events"] = map[string]any{}
		}
		eventReply(w, req.ID, map[string]any{"supportedVersions": supportedVersions, "capabilities": caps, "instructions": queryInstructions, "ttlMs": 0, "cacheScope": "private"}, nil)
		return true
	}
	if events != nil && strings.HasPrefix(req.Method, "events/") {
		encoded, _ := json.Marshal(req)
		return events.handle(w, r, owner, encoded)
	}
	switch req.Method {
	case "tools/list":
		var p struct {
			Cursor string `json:"cursor,omitempty"`
		}
		if decodeEventParams(req.Params, &p) != nil || p.Cursor != "" {
			fail(400, -32602, "invalid cursor")
			return true
		}
		names := make([]string, 0, len(tools))
		for name := range tools {
			names = append(names, name)
		}
		sort.Strings(names)
		definitions := make([]any, 0, len(names))
		for _, name := range names {
			definitions = append(definitions, tools[name].definition)
		}
		eventReply(w, req.ID, map[string]any{"tools": definitions, "ttlMs": 0, "cacheScope": "private"}, nil)
	case "tools/call":
		var p struct {
			Name           string                     `json:"name"`
			Arguments      json.RawMessage            `json:"arguments,omitempty"`
			InputResponses map[string]json.RawMessage `json:"inputResponses,omitempty"`
			RequestState   json.RawMessage            `json:"requestState,omitempty"`
		}
		if decodeEventParams(req.Params, &p) != nil {
			fail(400, -32602, "invalid tool params")
			return true
		}
		tool, ok := tools[p.Name]
		if !ok {
			fail(400, -32602, "unknown tool")
			return true
		}
		out, err := tool.call(r.Context(), p.Arguments)
		if err != nil {
			eventReply(w, req.ID, map[string]any{"content": []any{map[string]string{"type": "text", "text": err.Error()}}, "isError": true}, nil)
			return true
		}
		text, _ := json.Marshal(out)
		eventReply(w, req.ID, map[string]any{"content": []any{map[string]string{"type": "text", "text": string(text)}}, "structuredContent": out, "isError": false}, nil)
	default:
		fail(404, -32601, "method not found")
	}
	return true
}
