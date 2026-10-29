package pipeline

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

func TestSMTPMailer_MailHogOrMailpitIntegration(t *testing.T) {
	host := strings.TrimSpace(os.Getenv("MAIL_SMTP_HOST"))
	if host == "" {
		host = "127.0.0.1"
	}

	port := 1025
	if p := strings.TrimSpace(os.Getenv("MAIL_SMTP_PORT")); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("invalid MAIL_SMTP_PORT: %v", err)
		}
		port = parsed
	}

	username := strings.TrimSpace(os.Getenv("MAIL_SMTP_USERNAME"))
	password := strings.TrimSpace(os.Getenv("MAIL_SMTP_PASSWORD"))
	if username == "" || password == "" {
		t.Skip("set MAIL_SMTP_USERNAME and MAIL_SMTP_PASSWORD to run MailHog/Mailpit SMTP integration test")
	}

	encryption := strings.TrimSpace(os.Getenv("MAIL_SMTP_ENCRYPTION"))
	if encryption == "" {
		encryption = "none"
	}

	cfg := tenant.SMTPSettings{
		Enabled:    true,
		Provider:   "custom",
		Host:       host,
		Port:       port,
		Username:   username,
		Password:   password,
		Encryption: encryption,
		FromEmail:  "noreply@example.com",
		FromName:   "SupaCraft",
	}

	mailer := NewSMTPMailer()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := mailer.Send(ctx, cfg, MailMessage{
		FromEmail: cfg.FromEmail,
		FromName:  cfg.FromName,
		To:        []string{"integration@example.com"},
		Subject:   fmt.Sprintf("supacraft integration %d", time.Now().UnixNano()),
		TextBody:  "mailhog/mailpit integration check",
	})
	if err != nil {
		if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no route to host") {
			t.Skipf("mailhog/mailpit not reachable at %s:%d: %v", host, port, err)
		}
		t.Fatalf("smtp integration send failed: %v", err)
	}
}
