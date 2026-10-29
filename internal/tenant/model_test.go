package tenant

import (
	"encoding/json"
	"testing"
)

func TestTenantConfig_UnmarshalSMTPSettings(t *testing.T) {
	raw := []byte(`{
		"db": {
			"host": "localhost",
			"port": 5432,
			"name": "acme",
			"username": "db_user",
			"password": "encrypted-db-pass"
		},
		"auth_enabled": true,
		"schema": {
			"tenant_id": "tenant-1",
			"tables": [],
			"relations": []
		},
		"api_settings": {
			"email": {
				"smtp": {
					"enabled": true,
					"provider": "resend",
					"host": "smtp.resend.com",
					"port": 587,
					"username": "apikey",
					"password": "runtime-decrypted",
					"encryption": "tls",
					"from_email": "noreply@example.com",
					"from_name": "SupaCraft"
				}
			}
		},
		"display_name": "Acme",
		"updated_at": "2026-03-10T10:00:00Z"
	}`)

	var cfg TenantConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal tenant config: %v", err)
	}

	smtp := cfg.APISettings.Email.SMTP
	if !smtp.Enabled {
		t.Fatalf("expected smtp.enabled=true")
	}
	if smtp.Host != "smtp.resend.com" {
		t.Fatalf("unexpected smtp.host: %q", smtp.Host)
	}
	if smtp.Password != "runtime-decrypted" {
		t.Fatalf("unexpected smtp.password: %q", smtp.Password)
	}
	if smtp.FromEmail != "noreply@example.com" || smtp.FromName != "SupaCraft" {
		t.Fatalf("unexpected sender: %#v", smtp)
	}
}

func TestFunctionStep_UnmarshalEmailSendStep(t *testing.T) {
	raw := []byte(`{
		"type": "email.send",
		"mode": "template",
		"to": ["{{user.email}}"],
		"cc": ["ops@example.com"],
		"bcc": ["audit@example.com"],
		"subject": "Welcome {{user.name}}",
		"template_id": "welcome-email",
		"data": {"name": "{{user.name}}"},
		"reply_to": "support@example.com",
		"assign": "emailResult"
	}`)

	var step FunctionStep
	if err := json.Unmarshal(raw, &step); err != nil {
		t.Fatalf("unmarshal function step: %v", err)
	}

	if step.Type != "email.send" || step.Mode != "template" {
		t.Fatalf("unexpected type/mode: %q/%q", step.Type, step.Mode)
	}
	if step.TemplateID != "welcome-email" {
		t.Fatalf("unexpected template_id: %q", step.TemplateID)
	}
	if step.ReplyTo != "support@example.com" {
		t.Fatalf("unexpected reply_to: %q", step.ReplyTo)
	}
	if step.Assign != "emailResult" {
		t.Fatalf("unexpected assign: %q", step.Assign)
	}
	if len(step.To) != 1 || step.To[0] != "{{user.email}}" {
		t.Fatalf("unexpected to: %#v", step.To)
	}
}
