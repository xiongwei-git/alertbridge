package mcpapi

import (
	"egresshub/client"
	"errors"
	"net"
)

// ConfigureEgress opts only the MCP Events callback client into EgressHub.
// The SDK owns TLS, CONNECT, atomic credential loading and fail-closed transport.
// Application callback policy, signatures and durable retries stay in AlertBridge.
func (e *Events) ConfigureEgress(path string) error {
	if path == "" {
		return nil
	}
	cfg, err := client.LoadConfig(path)
	if err != nil {
		return err
	}
	for _, authority := range cfg.Destinations {
		host, port, err := net.SplitHostPort(authority)
		if err != nil || port != "443" || !e.hosts[host] {
			return errors.New("MCP Events egress destination exceeds callback policy")
		}
	}
	c, err := client.NewHTTPClient(cfg)
	if err != nil {
		return err
	}
	e.client.CloseIdleConnections()
	e.client = c
	return nil
}
