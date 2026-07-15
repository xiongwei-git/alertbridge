package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Status string

const (
	StatusFiring   Status = "firing"
	StatusResolved Status = "resolved"
	StatusInfo     Status = "info"
	StatusTest     Status = "test"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

type Event struct {
	EventID    string            `json:"event_id"`
	Source     string            `json:"source"`
	RoutingKey string            `json:"routing_key"`
	Status     Status            `json:"status"`
	Severity   Severity          `json:"severity"`
	Title      string            `json:"title"`
	Message    string            `json:"message"`
	OccurredAt time.Time         `json:"occurred_at"`
	DedupeKey  string            `json:"dedupe_key,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	URL        string            `json:"url,omitempty"`

	IncidentStartedAt *time.Time `json:"-"`
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func (e Event) Validate(now time.Time) error {
	if err := validateIdentifier("event_id", e.EventID, 128); err != nil {
		return err
	}
	if err := validateIdentifier("source", e.Source, 64); err != nil {
		return err
	}
	if err := validateIdentifier("routing_key", e.RoutingKey, 64); err != nil {
		return err
	}
	switch e.Status {
	case StatusFiring, StatusResolved, StatusInfo, StatusTest:
	default:
		return fmt.Errorf("status must be firing, resolved, info, or test")
	}
	switch e.Severity {
	case SeverityCritical, SeverityWarning, SeverityInfo:
	default:
		return fmt.Errorf("severity must be critical, warning, or info")
	}
	if strings.TrimSpace(e.Title) == "" || len([]rune(e.Title)) > 200 {
		return fmt.Errorf("title must contain 1 to 200 characters")
	}
	if strings.ContainsAny(e.Title, "\r\n") {
		return fmt.Errorf("title must not contain line breaks")
	}
	if strings.TrimSpace(e.Message) == "" || len([]rune(e.Message)) > 4000 {
		return fmt.Errorf("message must contain 1 to 4000 characters")
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("occurred_at is required")
	}
	if e.OccurredAt.After(now.Add(5 * time.Minute)) {
		return fmt.Errorf("occurred_at must not be more than 5 minutes in the future")
	}
	if e.Status == StatusFiring || e.Status == StatusResolved {
		if err := validateIdentifier("dedupe_key", e.DedupeKey, 128); err != nil {
			return err
		}
	} else if e.DedupeKey != "" {
		if err := validateIdentifier("dedupe_key", e.DedupeKey, 128); err != nil {
			return err
		}
	}
	if len(e.Labels) > 20 {
		return fmt.Errorf("labels must not contain more than 20 entries")
	}
	for key, value := range e.Labels {
		if err := validateIdentifier("label key", key, 64); err != nil {
			return err
		}
		if len([]rune(value)) > 256 {
			return fmt.Errorf("label %q value must not exceed 256 characters", key)
		}
	}
	if e.URL != "" {
		u, err := url.Parse(e.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return fmt.Errorf("url must be an absolute HTTP or HTTPS URL without user information")
		}
	}
	return nil
}

func validateIdentifier(name, value string, max int) error {
	if value == "" || len(value) > max || !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s must match %s and contain at most %d bytes", name, identifierPattern.String(), max)
	}
	return nil
}

func (e Event) SortedLabelKeys() []string {
	keys := make([]string, 0, len(e.Labels))
	for key := range e.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
