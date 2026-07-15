package channel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

type SMTPConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	Recipients []string
	Mode       string
	Timeout    time.Duration
}

type SMTPSender struct{ cfg SMTPConfig }

func NewSMTPSender(cfg SMTPConfig) *SMTPSender {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &SMTPSender{cfg: cfg}
}

func (s *SMTPSender) Send(ctx context.Context, event domain.Event) (int, error) {
	message, from, recipients, err := buildEmail(s.cfg, event)
	if err != nil {
		return 0, &SendError{Message: "invalid email configuration", Retryable: false}
	}
	address := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	dialer := &net.Dialer{Timeout: s.cfg.Timeout}
	var connection net.Conn
	if s.cfg.Mode == "tls" {
		connection, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", address)
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return 0, &SendError{Message: "SMTP connection failed", Retryable: true}
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(s.cfg.Timeout))
	client, err := smtp.NewClient(connection, s.cfg.Host)
	if err != nil {
		return 0, classifySMTP("initialize SMTP", err)
	}
	defer client.Close()
	if s.cfg.Mode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return 0, &SendError{Message: "SMTP server does not support STARTTLS", Retryable: false}
		}
		if err := client.StartTLS(&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return 0, classifySMTP("SMTP STARTTLS failed", err)
		}
	}
	if s.cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return 0, classifySMTP("SMTP authentication failed", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return 0, classifySMTP("SMTP sender rejected", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return 0, classifySMTP("SMTP recipient rejected", err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return 0, classifySMTP("SMTP data rejected", err)
	}
	if _, err := w.Write(message); err != nil {
		_ = w.Close()
		return 0, &SendError{Message: "SMTP write failed", Retryable: true}
	}
	if err := w.Close(); err != nil {
		return 0, classifySMTP("SMTP delivery failed", err)
	}
	if err := client.Quit(); err != nil {
		return 0, classifySMTP("SMTP quit failed", err)
	}
	return 250, nil
}

func buildEmail(cfg SMTPConfig, event domain.Event) ([]byte, string, []string, error) {
	if cfg.Host == "" || cfg.Port < 1 || cfg.Port > 65535 || (cfg.Mode != "tls" && cfg.Mode != "starttls") {
		return nil, "", nil, errors.New("invalid SMTP endpoint")
	}
	fromAddress, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, "", nil, err
	}
	if len(cfg.Recipients) == 0 {
		return nil, "", nil, errors.New("recipient required")
	}
	recipients := make([]string, 0, len(cfg.Recipients))
	display := make([]string, 0, len(cfg.Recipients))
	for _, value := range cfg.Recipients {
		address, err := mail.ParseAddress(strings.TrimSpace(value))
		if err != nil {
			return nil, "", nil, err
		}
		recipients, display = append(recipients, address.Address), append(display, address.String())
	}
	subject := mime.QEncoding.Encode("utf-8", safeHeader(fmt.Sprintf("[%s] %s", event.Severity, event.Title)))
	body := renderPlainText(event)
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", fromAddress.String(), strings.Join(display, ", "), subject, time.Now().UTC().Format(time.RFC1123Z), body)
	return []byte(message), fromAddress.Address, recipients, nil
}

func classifySMTP(message string, err error) error {
	retryable := true
	var protocolErr *textproto.Error
	if errors.As(err, &protocolErr) && protocolErr.Code >= 500 {
		retryable = false
	}
	return &SendError{Message: message, Retryable: retryable}
}
