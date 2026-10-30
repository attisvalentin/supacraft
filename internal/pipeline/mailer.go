package pipeline

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

// MailMessage contains all normalized fields required for SMTP delivery.
type MailMessage struct {
	FromEmail string
	FromName  string
	To        []string
	CC        []string
	BCC       []string
	ReplyTo   string
	Subject   string
	HTMLBody  string
	TextBody  string
}

// MailSendMeta captures provider-neutral metadata from a successful send.
type MailSendMeta struct {
	MessageID string
}

// Mailer abstracts email delivery so pipeline logic is testable.
type Mailer interface {
	Send(ctx context.Context, smtpCfg tenant.SMTPSettings, msg MailMessage) (*MailSendMeta, error)
}

// SMTPMailer sends mail directly via tenant-scoped SMTP settings.
type SMTPMailer struct {
	DialTimeout time.Duration
}

func NewSMTPMailer() *SMTPMailer {
	return &SMTPMailer{DialTimeout: 10 * time.Second}
}

func ValidateSMTPSettings(cfg tenant.SMTPSettings) error {
	if !cfg.Enabled {
		return errors.New("smtp is disabled")
	}

	if strings.TrimSpace(cfg.Provider) == "" {
		return errors.New("smtp provider is required")
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("smtp host is required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return errors.New("smtp port must be between 1 and 65535")
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return errors.New("smtp username is required")
	}
	if strings.TrimSpace(cfg.Password) == "" {
		return errors.New("smtp password is required")
	}
	encryption := strings.ToLower(strings.TrimSpace(cfg.Encryption))
	if encryption != "tls" && encryption != "ssl" && encryption != "none" {
		return fmt.Errorf("smtp encryption must be one of tls, ssl, none (got %q)", cfg.Encryption)
	}
	if strings.TrimSpace(cfg.FromEmail) == "" {
		return errors.New("smtp from_email is required")
	}
	if strings.TrimSpace(cfg.FromName) == "" {
		return errors.New("smtp from_name is required")
	}
	if _, err := mail.ParseAddress(cfg.FromEmail); err != nil {
		return fmt.Errorf("smtp from_email is invalid: %w", err)
	}

	return nil
}

func (m *SMTPMailer) Send(ctx context.Context, smtpCfg tenant.SMTPSettings, msg MailMessage) (*MailSendMeta, error) {
	if err := ValidateSMTPSettings(smtpCfg); err != nil {
		return nil, err
	}
	if len(msg.To) == 0 {
		return nil, errors.New("at least one recipient is required")
	}
	if strings.TrimSpace(msg.HTMLBody) == "" && strings.TrimSpace(msg.TextBody) == "" {
		return nil, errors.New("either html or text body is required")
	}

	raw, err := buildSMTPMessage(msg)
	if err != nil {
		return nil, err
	}

	allRecipients := uniqueStrings(append(append([]string{}, msg.To...), append(msg.CC, msg.BCC...)...))
	addr := fmt.Sprintf("%s:%d", smtpCfg.Host, smtpCfg.Port)
	encryption := strings.ToLower(strings.TrimSpace(smtpCfg.Encryption))

	client, err := m.connect(ctx, smtpCfg.Host, addr, encryption)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if ok, _ := client.Extension("AUTH"); !ok {
		return nil, errors.New("smtp server does not support AUTH")
	}
	auth := smtp.PlainAuth("", smtpCfg.Username, smtpCfg.Password, smtpCfg.Host)
	if err := client.Auth(auth); err != nil {
		return nil, fmt.Errorf("smtp auth failed: %w", err)
	}

	if err := client.Mail(smtpCfg.FromEmail); err != nil {
		return nil, fmt.Errorf("smtp MAIL FROM failed: %w", err)
	}
	for _, rcpt := range allRecipients {
		if err := client.Rcpt(rcpt); err != nil {
			return nil, fmt.Errorf("smtp RCPT TO failed for %q: %w", rcpt, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return nil, fmt.Errorf("smtp DATA failed: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("smtp write failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("smtp data close failed: %w", err)
	}
	if err := client.Quit(); err != nil {
		return nil, fmt.Errorf("smtp QUIT failed: %w", err)
	}

	return &MailSendMeta{}, nil
}

func (m *SMTPMailer) connect(ctx context.Context, host, addr, encryption string) (*smtp.Client, error) {
	dialTimeout := m.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 10 * time.Second
	}

	tlsConfig := &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}

	switch encryption {
	case "ssl":
		conn, err := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: dialTimeout}, Config: tlsConfig}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("smtp ssl dial failed: %w", err)
		}
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			return nil, fmt.Errorf("smtp client init failed: %w", err)
		}
		return client, nil
	case "tls", "none":
		conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("smtp dial failed: %w", err)
		}
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			return nil, fmt.Errorf("smtp client init failed: %w", err)
		}
		if encryption == "tls" {
			if ok, _ := client.Extension("STARTTLS"); !ok {
				client.Close()
				return nil, errors.New("smtp server does not support STARTTLS")
			}
			if err := client.StartTLS(tlsConfig); err != nil {
				client.Close()
				return nil, fmt.Errorf("smtp STARTTLS failed: %w", err)
			}
		}
		return client, nil
	default:
		return nil, fmt.Errorf("unsupported smtp encryption %q", encryption)
	}
}

// headerSafe removes line breaks from a header value. Subjects are often
// built from request data, and a CR/LF there would let a client append its
// own headers (e.g. Bcc) to the message.
func headerSafe(v string) string {
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(v)
}

func buildSMTPMessage(msg MailMessage) ([]byte, error) {
	if strings.TrimSpace(msg.FromEmail) == "" {
		return nil, errors.New("from email is required")
	}

	fromAddr := mail.Address{Name: strings.TrimSpace(msg.FromName), Address: strings.TrimSpace(msg.FromEmail)}

	headers := map[string]string{
		"From":         fromAddr.String(),
		"To":           strings.Join(msg.To, ", "),
		"Subject":      mime.QEncoding.Encode("utf-8", msg.Subject),
		"MIME-Version": "1.0",
		"Date":         time.Now().UTC().Format(time.RFC1123Z),
	}
	if len(msg.CC) > 0 {
		headers["Cc"] = strings.Join(msg.CC, ", ")
	}
	if strings.TrimSpace(msg.ReplyTo) != "" {
		headers["Reply-To"] = strings.TrimSpace(msg.ReplyTo)
	}

	var b bytes.Buffer
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := headerSafe(headers[k])
		if k == "Subject" {
			// Long subjects become several encoded words; put each on its
			// own continuation line to stay under SMTP's line limit.
			v = strings.ReplaceAll(v, "?= =?", "?=\r\n =?")
		}
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}

	hasHTML := strings.TrimSpace(msg.HTMLBody) != ""
	hasText := strings.TrimSpace(msg.TextBody) != ""

	switch {
	case hasHTML && hasText:
		boundary := fmt.Sprintf("supacraft-%d", time.Now().UnixNano())
		fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
		mw := multipart.NewWriter(&b)
		if err := mw.SetBoundary(boundary); err != nil {
			return nil, fmt.Errorf("multipart boundary error: %w", err)
		}

		textPart, err := mw.CreatePart(textHeaders())
		if err != nil {
			return nil, fmt.Errorf("create text part: %w", err)
		}
		if _, err := textPart.Write([]byte(msg.TextBody)); err != nil {
			return nil, fmt.Errorf("write text part: %w", err)
		}

		htmlPart, err := mw.CreatePart(htmlHeaders())
		if err != nil {
			return nil, fmt.Errorf("create html part: %w", err)
		}
		if _, err := htmlPart.Write([]byte(msg.HTMLBody)); err != nil {
			return nil, fmt.Errorf("write html part: %w", err)
		}
		if err := mw.Close(); err != nil {
			return nil, fmt.Errorf("close multipart: %w", err)
		}
	case hasHTML:
		fmt.Fprint(&b, "Content-Type: text/html; charset=UTF-8\r\n\r\n")
		fmt.Fprint(&b, msg.HTMLBody)
	case hasText:
		fmt.Fprint(&b, "Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		fmt.Fprint(&b, msg.TextBody)
	default:
		return nil, errors.New("email body is empty")
	}

	return b.Bytes(), nil
}

func textHeaders() textproto.MIMEHeader {
	return textproto.MIMEHeader{
		"Content-Type":              []string{"text/plain; charset=UTF-8"},
		"Content-Transfer-Encoding": []string{"8bit"},
	}
}

func htmlHeaders() textproto.MIMEHeader {
	return textproto.MIMEHeader{
		"Content-Type":              []string{"text/html; charset=UTF-8"},
		"Content-Transfer-Encoding": []string{"8bit"},
	}
}

func uniqueStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			continue
		}
		if _, exists := set[trimmed]; exists {
			continue
		}
		set[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
