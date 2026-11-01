package handler

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/attisvalentin/supacraft/internal/pipeline"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

const authEmailProject = `{
  "name": "demo",
  "auth_enabled": true,
  "api_settings": {
    "email": {
      "smtp": {"enabled": true, "provider": "smtp", "host": "smtp.example.com", "port": 587, "username": "u", "password": "p",
               "encryption": "tls", "from_email": "app@example.com", "from_name": "App"},
      "templates": {
        "reset": {"subject": "Reset your password", "text": "Open {{.link}} within {{.expires_in_minutes}} minutes"},
        "verify": {"subject": "Verify {{.email}}", "text": "Open {{.link}}"}
      }
    },
    "auth": {
      "password_reset": {"template": "reset", "url": "https://app.example.com/reset?token={token}"},
      "email_verification": {"template": "verify", "url": "https://app.example.com/verify?token={token}", "required": true}
    }
  },
  "functions": {
    "me": {"route": {"path": "/fn/me", "method": "GET", "auth_required": true},
           "steps": [{"type": "response", "data": {"id": "{{auth.id}}"}}]}
  }
}`

// fakeMailer records messages instead of sending them.
type fakeMailer struct {
	mu   sync.Mutex
	sent []pipeline.MailMessage
}

func (m *fakeMailer) Send(_ context.Context, _ tenant.SMTPSettings, msg pipeline.MailMessage) (*pipeline.MailSendMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return &pipeline.MailSendMeta{}, nil
}

var tokenInLink = regexp.MustCompile(`token=([0-9a-f]{64})`)

// lastToken returns the token of the last mail to email, and how many mails
// were sent in total.
func (m *fakeMailer) lastToken(t *testing.T, email string) (string, int) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if m.sent[i].To[0] == email {
			match := tokenInLink.FindStringSubmatch(m.sent[i].TextBody)
			if match == nil {
				t.Fatalf("no link in %q", m.sent[i].TextBody)
			}
			return match[1], len(m.sent)
		}
	}
	return "", len(m.sent)
}

func withFakeMailer(t *testing.T) *fakeMailer {
	m := &fakeMailer{}
	authMailer, runInBackground = m, func(f func()) { f() }
	t.Cleanup(func() {
		authMailer, runInBackground = pipeline.NewSMTPMailer(), func(f func()) { go f() }
	})
	return m
}

func TestAuthEmails_VerifyThenReset(t *testing.T) {
	mail := withFakeMailer(t)
	a := newTestAPIWith(t, authEmailProject)
	const email = "ann@example.com"

	// Registering mails a verification link; login waits for it.
	a.register(email)
	verifyToken, n := mail.lastToken(t, email)
	if verifyToken == "" || n != 1 {
		t.Fatalf("verification mail: %q (%d sent)", verifyToken, n)
	}
	if code, _, raw := a.do("POST", "/auth/login", `{"email":"`+email+`","password":"Secret123!"}`); code != http.StatusForbidden {
		t.Fatalf("unverified login: %d %s", code, raw)
	}
	// A wrong password still says only "invalid credentials".
	if code, _, _ := a.do("POST", "/auth/login", `{"email":"`+email+`","password":"wrong-pass"}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong password on an unverified account: %d", code)
	}
	if code, _, raw := a.do("POST", "/auth/verify-email", `{"token":"`+verifyToken+`"}`); code != http.StatusOK {
		t.Fatalf("verify: %d %s", code, raw)
	}
	if code, _, _ := a.do("POST", "/auth/verify-email", `{"token":"`+verifyToken+`"}`); code != http.StatusBadRequest {
		t.Fatalf("verification token worked twice: %d", code)
	}
	// Verified users get no more verification mails.
	a.do("POST", "/auth/resend-verification", `{"email":"`+email+`"}`)
	if _, n := mail.lastToken(t, email); n != 1 {
		t.Fatalf("verified user got another verification mail (%d sent)", n)
	}
	session := a.login(email)

	// Unknown emails get the same answer and no mail.
	code, _, unknownRaw := a.do("POST", "/auth/forgot-password", `{"email":"nobody@example.com"}`)
	if code != http.StatusAccepted {
		t.Fatalf("forgot for an unknown email: %d", code)
	}
	code, _, knownRaw := a.do("POST", "/auth/forgot-password", `{"email":"`+email+`"}`)
	if code != http.StatusAccepted || knownRaw != unknownRaw {
		t.Fatalf("forgot answers differ: %q vs %q", knownRaw, unknownRaw)
	}
	resetToken, n := mail.lastToken(t, email)
	if n != 2 {
		t.Fatalf("reset mail not sent (%d sent)", n)
	}
	// A second request within a minute sends nothing.
	a.do("POST", "/auth/forgot-password", `{"email":"`+email+`"}`)
	if _, n := mail.lastToken(t, email); n != 2 {
		t.Fatalf("reset mails not throttled (%d sent)", n)
	}

	if code, _, raw := a.do("POST", "/auth/reset-password", `{"token":"`+resetToken+`","password":"short"}`); code != http.StatusBadRequest || !strings.Contains(raw, "at least") {
		t.Fatalf("weak password accepted: %d %s", code, raw)
	}
	if code, _, raw := a.do("POST", "/auth/reset-password", `{"token":"`+resetToken+`","password":"NewSecret456!"}`); code != http.StatusOK {
		t.Fatalf("reset: %d %s", code, raw)
	}
	if code, _, _ := a.do("POST", "/auth/reset-password", `{"token":"`+resetToken+`","password":"Other789!x"}`); code != http.StatusBadRequest {
		t.Fatalf("reset token worked twice: %d", code)
	}

	// Sessions from before the reset are over.
	if code, _, raw := a.do("GET", "/fn/me", "", "Authorization", "Bearer "+session["token"].(string)); code != http.StatusUnauthorized {
		t.Fatalf("old access token still works: %d %s", code, raw)
	}
	if code, _, _ := a.do("POST", "/auth/refresh", `{"refresh_token":"`+session["refresh_token"].(string)+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("old refresh token still works: %d", code)
	}
	if code, _, _ := a.do("POST", "/auth/login", `{"email":"`+email+`","password":"Secret123!"}`); code != http.StatusUnauthorized {
		t.Fatal("old password still works")
	}
	code, out, raw := a.do("POST", "/auth/login", `{"email":"`+email+`","password":"NewSecret456!"}`)
	if code != http.StatusOK {
		t.Fatalf("login with the new password: %d %s", code, raw)
	}
	fresh := out["data"].(map[string]any)
	if code, _, raw := a.do("GET", "/fn/me", "", "Authorization", "Bearer "+fresh["token"].(string)); code != http.StatusOK {
		t.Fatalf("new access token: %d %s", code, raw)
	}
	if code, _, raw := a.do("POST", "/auth/refresh", `{"refresh_token":"`+fresh["refresh_token"].(string)+`"}`); code != http.StatusOK {
		t.Fatalf("new refresh token: %d %s", code, raw)
	}
}

func TestAuthEmails_EndpointsOffWithoutConfig(t *testing.T) {
	a := newTestAPIWith(t, `{"name": "demo", "auth_enabled": true}`)
	for _, p := range []string{"/auth/forgot-password", "/auth/reset-password", "/auth/verify-email", "/auth/resend-verification"} {
		if code, _, _ := a.do("POST", p, `{"email":"a@b.co","token":"x"}`); code != http.StatusNotFound {
			t.Errorf("%s answered %d without configuration", p, code)
		}
	}
}
