package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
)

func main() {
	var count atomic.Int64
	var lastPayload atomic.Value
	lastPayload.Store([]byte(`{}`))
	mux := http.NewServeMux()
	mux.HandleFunc("/hook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 32*1024))
		if err != nil {
			http.Error(w, "body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if !bytes.Contains(body, []byte("AlertBridge")) {
			_, _ = w.Write([]byte(`{"code":19024,"msg":"Key Words Not Found"}`))
			return
		}
		lastPayload.Store(bytes.Clone(body))
		count.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	})
	mux.HandleFunc("/last", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(lastPayload.Load().([]byte))
	})
	mux.HandleFunc("/count", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"count": count.Load()})
	})
	_ = http.ListenAndServe(":9090", mux)
}
