package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/channel"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

type Config struct {
	PollInterval    time.Duration
	LeaseDuration   time.Duration
	RetryDelays     []time.Duration
	MaxAttempts     int
	Retention       time.Duration
	SenderFor       func(string) (channel.Sender, bool)
	DisplayLocation *time.Location
}

type Worker struct {
	store     *store.Store
	senders   map[string]channel.Sender
	senderFor func(string) (channel.Sender, bool)
	cfg       Config
	logger    *slog.Logger
}

func New(database *store.Store, senders map[string]channel.Sender, cfg Config) *Worker {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = 30 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 6
	}
	if len(cfg.RetryDelays) == 0 {
		cfg.RetryDelays = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 30 * 24 * time.Hour
	}
	if cfg.DisplayLocation == nil {
		cfg.DisplayLocation = time.UTC
	}
	senderFor := cfg.SenderFor
	if senderFor == nil {
		senderFor = func(id string) (channel.Sender, bool) { sender, ok := senders[id]; return sender, ok }
	}
	return &Worker{store: database, senders: senders, senderFor: senderFor, cfg: cfg, logger: slog.Default()}
}

func (w *Worker) WithLogger(logger *slog.Logger) *Worker { w.logger = logger; return w }

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	nextCleanup := time.Time{}
	for {
		now := time.Now().UTC()
		if !now.Before(nextCleanup) {
			deleted, cleanupErr := w.store.Prune(ctx, now.Add(-w.cfg.Retention))
			if cleanupErr != nil && !errors.Is(cleanupErr, context.Canceled) {
				w.logger.Error("event retention cleanup failed", "error", cleanupErr)
			}
			if deleted > 0 {
				w.logger.Info("expired event records pruned", "count", deleted)
			}
			nextCleanup = now.Add(time.Hour)
		}
		processed, err := w.ProcessOne(ctx, now)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Error("delivery worker error", "error", err)
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) ProcessOne(ctx context.Context, now time.Time) (bool, error) {
	delivery, err := w.store.ClaimDelivery(ctx, now, w.cfg.LeaseDuration)
	if err != nil || delivery == nil {
		return false, err
	}
	sender, ok := w.senderFor(delivery.ChannelID)
	if !ok {
		err := w.store.CompleteFailure(ctx, delivery.ID, delivery.Attempts, now, "channel is unavailable", 0, true)
		return true, err
	}
	event := delivery.Event
	event.OccurredAt = event.OccurredAt.In(w.cfg.DisplayLocation)
	if event.IncidentStartedAt != nil {
		started := event.IncidentStartedAt.In(w.cfg.DisplayLocation)
		event.IncidentStartedAt = &started
	}
	statusCode, sendErr := sender.Send(ctx, event)
	if sendErr == nil {
		if err := w.store.CompleteSuccess(ctx, delivery.ID, statusCode, now); err != nil {
			return true, err
		}
		w.logger.Info("notification sent", "delivery_id", delivery.ID, "channel", delivery.ChannelID, "attempt", delivery.Attempts)
		return true, nil
	}
	retryable := true
	var typed *channel.SendError
	if errors.As(sendErr, &typed) {
		retryable = typed.Retryable
		if statusCode == 0 {
			statusCode = typed.StatusCode
		}
	}
	dead := !retryable || delivery.Attempts >= w.cfg.MaxAttempts
	next := now
	if !dead {
		index := delivery.Attempts - 1
		if index >= len(w.cfg.RetryDelays) {
			index = len(w.cfg.RetryDelays) - 1
		}
		next = now.Add(w.cfg.RetryDelays[index])
	}
	message := fmt.Sprintf("delivery failed: %v", sendErr)
	if err := w.store.CompleteFailure(ctx, delivery.ID, delivery.Attempts, next, message, statusCode, dead); err != nil {
		return true, err
	}
	w.logger.Warn("notification delivery failed", "delivery_id", delivery.ID, "channel", delivery.ChannelID, "attempt", delivery.Attempts, "dead", dead, "error", sendErr)
	return true, nil
}
