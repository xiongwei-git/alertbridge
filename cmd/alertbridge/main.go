package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/admin"
	"github.com/xiongwei-git/alertbridge/internal/auth"
	"github.com/xiongwei-git/alertbridge/internal/channel"
	"github.com/xiongwei-git/alertbridge/internal/config"
	"github.com/xiongwei-git/alertbridge/internal/httpapi"
	"github.com/xiongwei-git/alertbridge/internal/runtimecfg"
	"github.com/xiongwei-git/alertbridge/internal/securestore"
	"github.com/xiongwei-git/alertbridge/internal/store"
	"github.com/xiongwei-git/alertbridge/internal/worker"
)

var version = "dev"

func main() {
	syscall.Umask(0o077)
	configPath := flag.String("config", envOr("ALERTBRIDGE_CONFIG", "/etc/alertbridge/config.json"), "configuration file path")
	healthcheck := flag.Bool("healthcheck", false, "check the local readiness endpoint")
	flag.Parse()
	if *healthcheck {
		if err := runHealthcheck(); err != nil {
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	database, err := store.Open(cfg.Database.Path)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	clients := make(map[string]auth.Client, len(cfg.Clients))
	for id, item := range cfg.Clients {
		routes := make(map[string]struct{}, len(item.AllowedRoutes))
		for _, route := range item.AllowedRoutes {
			routes[route] = struct{}{}
		}
		clients[id] = auth.Client{ID: id, Secret: item.Secret, Enabled: item.Enabled, AllowedRoutes: routes, RateLimitPerMin: item.RateLimitPerMinute}
	}
	senders := make(map[string]channel.Sender)
	enabled := make(map[string]bool, len(cfg.Channels))
	for id, item := range cfg.Channels {
		enabled[id] = item.Enabled
		if !item.Enabled {
			continue
		}
		client := channel.SecureHTTPClient(cfg.Worker.RequestTimeout)
		senders[id] = channel.NewFeishuSender(channel.FeishuConfig{Webhook: item.Webhook, SigningSecret: item.SigningSecret, MessageType: item.MessageType, Keyword: item.Keyword, Client: client})
	}
	verifier := auth.Verifier{Clients: clients, Tolerance: cfg.Auth.TimestampTolerance}
	var resolveTargets func(string, string) []string
	var isSilenced func(string, string, time.Time) bool
	var senderFor func(string) (channel.Sender, bool)
	var adminHandler http.Handler
	if cfg.Admin.Enabled {
		cipher, cipherErr := securestore.New(cfg.Admin.MasterKey)
		if cipherErr != nil {
			logger.Error("initialize secure configuration store", "error", cipherErr)
			os.Exit(1)
		}
		gateway, gatewayErr := runtimecfg.New(ctx, runtimecfg.Options{Database: database, Cipher: cipher, Bootstrap: cfg, RequestTimeout: cfg.Worker.RequestTimeout, AllowInsecureHTTP: os.Getenv("ALERTBRIDGE_ALLOW_INSECURE_HTTP") == "1", Logger: logger})
		if gatewayErr != nil {
			logger.Error("initialize dynamic configuration", "error", gatewayErr)
			os.Exit(1)
		}
		adminUI, adminErr := admin.New(admin.Config{Database: database, Gateway: gateway, Username: cfg.Admin.Username, Password: cfg.Admin.Password, SessionLifetime: cfg.Admin.SessionLifetime, SecureCookie: cfg.Admin.SecureCookie, Logger: logger})
		if adminErr != nil {
			logger.Error("initialize admin console", "error", adminErr)
			os.Exit(1)
		}
		verifier.Clients = nil
		verifier.Lookup = gateway.LookupClient
		resolveTargets = gateway.ResolveTargets
		isSilenced = gateway.IsSilenced
		senderFor = gateway.Sender
		adminHandler = adminUI
	}
	handler := httpapi.New(httpapi.Config{Database: database, Verifier: verifier, Admin: adminHandler, Routes: cfg.Routes, EnabledChannels: enabled, ResolveTargets: resolveTargets, IsSilenced: isSilenced, NonceRetention: cfg.Auth.NonceRetention, DedupeWindow: cfg.Dedupe.Window, BodyLimitBytes: cfg.Server.BodyLimitBytes, Logger: logger})
	server := &http.Server{Addr: cfg.Server.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	deliveryWorker := worker.New(database, senders, worker.Config{PollInterval: cfg.Worker.PollInterval, LeaseDuration: cfg.Worker.LeaseDuration, RetryDelays: cfg.Worker.RetryDelays, MaxAttempts: cfg.Worker.MaxAttempts, Retention: cfg.Database.Retention, SenderFor: senderFor}).WithLogger(logger)
	go deliveryWorker.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP shutdown", "error", err)
		}
	}()
	logger.Info("AlertBridge started", "version", version, "listen", cfg.Server.Listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("AlertBridge stopped")
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func runHealthcheck() error {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://127.0.0.1:8080/readyz")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("readiness check failed")
	}
	return nil
}
