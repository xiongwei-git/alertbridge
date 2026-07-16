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
	"github.com/xiongwei-git/alertbridge/internal/bootstrap"
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
	cfg, err := config.Load()
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

	boot, err := bootstrap.Initialize(ctx, database, bootstrap.Options{Username: cfg.Admin.Username, PasswordFile: cfg.Admin.PasswordFile, MasterKeyPath: cfg.Admin.MasterKeyPath})
	if err != nil {
		logger.Error("initialize secure bootstrap", "error", err)
		os.Exit(1)
	}
	cipher, err := securestore.New(boot.MasterKey)
	if err != nil {
		logger.Error("initialize secure configuration store", "error", err)
		os.Exit(1)
	}
	gateway, err := runtimecfg.New(ctx, runtimecfg.Options{Database: database, Cipher: cipher, RequestTimeout: cfg.Worker.RequestTimeout, AllowInsecureHTTP: os.Getenv("ALERTBRIDGE_ALLOW_INSECURE_HTTP") == "1", Logger: logger})
	if err != nil {
		logger.Error("initialize dynamic configuration", "error", err)
		os.Exit(1)
	}
	adminUI, err := admin.New(admin.Config{Database: database, Gateway: gateway, Username: boot.Credential.Username, PasswordHash: boot.Credential.PasswordHash, SessionLifetime: cfg.Admin.SessionLifetime, SecureCookie: cfg.Admin.SecureCookie, DisplayLocation: cfg.Display.Location, Logger: logger})
	if err != nil {
		logger.Error("initialize admin console", "error", err)
		os.Exit(1)
	}
	logger.Info("secure bootstrap ready", "admin_created", boot.AdminCreated, "master_key_created", boot.MasterKeyCreated)
	verifier := auth.Verifier{Lookup: gateway.LookupClient, Tolerance: cfg.Auth.TimestampTolerance}
	handler := httpapi.New(httpapi.Config{Database: database, Verifier: verifier, Admin: adminUI, ResolveTargets: gateway.ResolveTargets, IsSilenced: gateway.IsSilenced, NonceRetention: cfg.Auth.NonceRetention, DedupeWindow: cfg.Dedupe.Window, BodyLimitBytes: cfg.Server.BodyLimitBytes, Logger: logger})
	server := &http.Server{Addr: cfg.Server.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	deliveryWorker := worker.New(database, nil, worker.Config{PollInterval: cfg.Worker.PollInterval, LeaseDuration: cfg.Worker.LeaseDuration, RetryDelays: cfg.Worker.RetryDelays, MaxAttempts: cfg.Worker.MaxAttempts, Retention: cfg.Database.Retention, SenderFor: gateway.Sender, DisplayLocation: cfg.Display.Location}).WithLogger(logger)
	go deliveryWorker.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP shutdown", "error", err)
		}
	}()
	logger.Info("AlertBridge started", "version", version, "listen", cfg.Server.Listen, "display_timezone", cfg.Display.TimeZone)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("AlertBridge stopped")
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
