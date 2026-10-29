package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

type fakeMailer struct {
	calls    []mailCall
	sendMeta *MailSendMeta
	sendErr  error
}

type mailCall struct {
	smtpCfg tenant.SMTPSettings
	msg     MailMessage
}

func (m *fakeMailer) Send(ctx context.Context, smtpCfg tenant.SMTPSettings, msg MailMessage) (*MailSendMeta, error) {
	m.calls = append(m.calls, mailCall{smtpCfg: smtpCfg, msg: msg})
	if m.sendErr != nil {
		return nil, m.sendErr
	}
	if m.sendMeta != nil {
		return m.sendMeta, nil
	}
	return &MailSendMeta{}, nil
}

func baseTenantConfig() *tenant.TenantConfig {
	return &tenant.TenantConfig{
		DBName: "tenant_acme",
		APISettings: tenant.APISettings{
			Email: tenant.EmailSettings{
				SMTP: tenant.SMTPSettings{
					Enabled:    true,
					Provider:   "custom",
					Host:       "smtp.example.com",
					Port:       587,
					Username:   "smtp-user",
					Password:   "smtp-pass",
					Encryption: "tls",
					FromEmail:  "noreply@example.com",
					FromName:   "SupaCraft",
				},
				Templates: map[string]tenant.EmailTemplate{
					"welcome-email": {
						Subject: "Welcome {{.name}}",
						HTML:    "<p>Hello {{.name}}</p>",
						Text:    "Hello {{.name}}",
					},
				},
			},
		},
	}
}

func TestExecuteEmailSend_Validation(t *testing.T) {
	tests := []struct {
		name     string
		step     tenant.FunctionStep
		mutate   func(*tenant.TenantConfig)
		wantErr  string
		setupCtx func(*PipelineContext)
	}{
		{
			name:    "to required",
			step:    tenant.FunctionStep{Type: "email.send", Mode: "raw", Subject: "x", HTML: "<p>ok</p>"},
			wantErr: "to is required",
		},
		{
			name:    "template mode requires template_id",
			step:    tenant.FunctionStep{Type: "email.send", Mode: "template", To: []string{"ops@example.com"}},
			wantErr: "template_id is required in template mode",
		},
		{
			name:    "raw mode requires html or text",
			step:    tenant.FunctionStep{Type: "email.send", Mode: "raw", To: []string{"ops@example.com"}},
			wantErr: "html or text is required in raw mode",
		},
		{
			name: "disabled smtp rejected",
			step: tenant.FunctionStep{Type: "email.send", Mode: "raw", To: []string{"ops@example.com"}, Text: "hello"},
			mutate: func(cfg *tenant.TenantConfig) {
				cfg.APISettings.Email.SMTP.Enabled = false
			},
			wantErr: "invalid smtp config: smtp is disabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseTenantConfig()
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			deps := &StepDeps{
				Config:       cfg,
				Mailer:       &fakeMailer{},
				FunctionID:   "func_test",
				FunctionName: "test",
			}
			pCtx := NewPipelineContext()
			if tt.setupCtx != nil {
				tt.setupCtx(pCtx)
			}

			err := ExecuteEmailSend(context.Background(), &tt.step, pCtx, deps)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestExecuteEmailSend_InterpolatesRawMode(t *testing.T) {
	mailer := &fakeMailer{sendMeta: &MailSendMeta{MessageID: "msg-123"}}
	cfg := baseTenantConfig()
	deps := &StepDeps{
		Config:       cfg,
		Mailer:       mailer,
		FunctionID:   "func_email_raw",
		FunctionName: "send raw",
	}

	pCtx := NewPipelineContext()
	pCtx.Set("user", map[string]any{
		"email": "john@example.com",
		"name":  "John",
		"id":    "u_42",
	})
	pCtx.Set("extraRecipients", []any{"ops@example.com", "audit@example.com"})

	step := tenant.FunctionStep{
		Type:    "email.send",
		Mode:    "raw",
		To:      []string{"{{user.email}}", "{{extraRecipients}}", "team+{{user.id}}@example.com"},
		Subject: "Alert for {{user.name}}",
		HTML:    "<p>Hello {{user.name}}</p>",
		Text:    "Hello {{user.name}}",
		Assign:  "emailResult",
	}

	if err := ExecuteEmailSend(context.Background(), &step, pCtx, deps); err != nil {
		t.Fatalf("execute email.send: %v", err)
	}

	if len(mailer.calls) != 1 {
		t.Fatalf("expected one mailer call, got %d", len(mailer.calls))
	}
	call := mailer.calls[0]
	if call.msg.Subject != "Alert for John" {
		t.Fatalf("unexpected subject: %q", call.msg.Subject)
	}
	if call.msg.TextBody != "Hello John" {
		t.Fatalf("unexpected text body: %q", call.msg.TextBody)
	}
	if !strings.Contains(call.msg.HTMLBody, "Hello John") {
		t.Fatalf("unexpected html body: %q", call.msg.HTMLBody)
	}
	if len(call.msg.To) != 4 {
		t.Fatalf("unexpected recipients: %#v", call.msg.To)
	}

	assignedRaw, ok := pCtx.Get("emailResult")
	if !ok {
		t.Fatalf("expected assign variable emailResult")
	}
	assigned, ok := assignedRaw.(map[string]any)
	if !ok {
		t.Fatalf("expected assigned result map, got %T", assignedRaw)
	}
	if assigned["mode"] != "raw" {
		t.Fatalf("unexpected mode in assigned result: %#v", assigned)
	}
	if assigned["message_id"] != "msg-123" {
		t.Fatalf("expected message_id in assigned result: %#v", assigned)
	}
}

func TestExecuteEmailSend_TemplateMode(t *testing.T) {
	mailer := &fakeMailer{}
	cfg := baseTenantConfig()
	deps := &StepDeps{
		Config:       cfg,
		Mailer:       mailer,
		FunctionID:   "func_email_tpl",
		FunctionName: "send template",
	}

	pCtx := NewPipelineContext()
	pCtx.Set("user", map[string]any{
		"email": "john@example.com",
		"name":  "John",
	})

	step := tenant.FunctionStep{
		Type:       "email.send",
		Mode:       "template",
		TemplateID: "welcome-email",
		To:         []string{"{{user.email}}"},
		Data: map[string]any{
			"name": "{{user.name}}",
		},
		Assign: "emailResult",
	}

	if err := ExecuteEmailSend(context.Background(), &step, pCtx, deps); err != nil {
		t.Fatalf("execute template email.send: %v", err)
	}

	if len(mailer.calls) != 1 {
		t.Fatalf("expected one mailer call, got %d", len(mailer.calls))
	}
	call := mailer.calls[0]
	if call.msg.Subject != "Welcome John" {
		t.Fatalf("unexpected rendered subject: %q", call.msg.Subject)
	}
	if call.msg.TextBody != "Hello John" {
		t.Fatalf("unexpected rendered text: %q", call.msg.TextBody)
	}
	if call.msg.HTMLBody != "<p>Hello John</p>" {
		t.Fatalf("unexpected rendered html: %q", call.msg.HTMLBody)
	}

	assignedRaw, ok := pCtx.Get("emailResult")
	if !ok {
		t.Fatalf("expected assign variable emailResult")
	}
	assigned := assignedRaw.(map[string]any)
	if assigned["template_id"] != "welcome-email" {
		t.Fatalf("unexpected template_id in assigned result: %#v", assigned)
	}
}
