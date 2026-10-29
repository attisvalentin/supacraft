package pipeline

import (
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"log/slog"
	"net/mail"
	"strings"
	texttemplate "text/template"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

func ExecuteEmailSend(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) (err error) {
	defer func() {
		if err != nil {
			logEmailSendFailure(deps, step, err)
		}
	}()

	if deps == nil || deps.Config == nil {
		return errors.New("email.send: missing tenant config")
	}
	if err := validateEmailSendStep(step); err != nil {
		return err
	}

	smtpCfg := deps.Config.APISettings.Email.SMTP
	if err := ValidateSMTPSettings(smtpCfg); err != nil {
		return fmt.Errorf("email.send: invalid smtp config: %w", err)
	}

	to, err := resolveRecipients(step.To, pCtx, "to", true)
	if err != nil {
		return fmt.Errorf("email.send: %w", err)
	}
	cc, err := resolveRecipients(step.CC, pCtx, "cc", false)
	if err != nil {
		return fmt.Errorf("email.send: %w", err)
	}
	bcc, err := resolveRecipients(step.BCC, pCtx, "bcc", false)
	if err != nil {
		return fmt.Errorf("email.send: %w", err)
	}

	subject, err := resolveStringValue(step.Subject, pCtx)
	if err != nil {
		return fmt.Errorf("email.send: subject interpolation failed: %w", err)
	}
	replyTo, err := resolveStringValue(step.ReplyTo, pCtx)
	if err != nil {
		return fmt.Errorf("email.send: reply_to interpolation failed: %w", err)
	}
	if strings.TrimSpace(replyTo) != "" {
		parsed, parseErr := mail.ParseAddress(replyTo)
		if parseErr != nil {
			return fmt.Errorf("email.send: invalid reply_to: %w", parseErr)
		}
		replyTo = parsed.Address
	}

	mode := strings.ToLower(strings.TrimSpace(step.Mode))
	htmlBody := ""
	textBody := ""
	templateID := ""

	switch mode {
	case "raw":
		htmlBody, err = resolveStringValue(step.HTML, pCtx)
		if err != nil {
			return fmt.Errorf("email.send: html interpolation failed: %w", err)
		}
		textBody, err = resolveStringValue(step.Text, pCtx)
		if err != nil {
			return fmt.Errorf("email.send: text interpolation failed: %w", err)
		}
		if strings.TrimSpace(htmlBody) == "" && strings.TrimSpace(textBody) == "" {
			return errors.New("email.send: raw mode requires html or text after interpolation")
		}
	case "template":
		templateID = strings.TrimSpace(step.TemplateID)
		tpl, tplErr := resolveTenantTemplate(deps.Config, templateID)
		if tplErr != nil {
			return fmt.Errorf("email.send: %w", tplErr)
		}

		resolvedData, dataErr := resolveTemplateData(step.Data, pCtx)
		if dataErr != nil {
			return fmt.Errorf("email.send: template data interpolation failed: %w", dataErr)
		}

		renderedSubject, renderedHTML, renderedText, renderErr := renderTenantTemplate(templateID, tpl, resolvedData)
		if renderErr != nil {
			return fmt.Errorf("email.send: template render failed: %w", renderErr)
		}
		if subject == "" {
			subject = renderedSubject
		}
		htmlBody = renderedHTML
		textBody = renderedText
		if strings.TrimSpace(htmlBody) == "" && strings.TrimSpace(textBody) == "" {
			return errors.New("email.send: resolved template has no html or text body")
		}
	default:
		return fmt.Errorf("email.send: invalid mode %q", step.Mode)
	}

	mailer := deps.Mailer
	if mailer == nil {
		mailer = NewSMTPMailer()
	}

	sendMeta, err := mailer.Send(ctx, smtpCfg, MailMessage{
		FromEmail: smtpCfg.FromEmail,
		FromName:  smtpCfg.FromName,
		To:        to,
		CC:        cc,
		BCC:       bcc,
		ReplyTo:   replyTo,
		Subject:   subject,
		HTMLBody:  htmlBody,
		TextBody:  textBody,
	})
	if err != nil {
		return fmt.Errorf("email.send: smtp send failed: %w", err)
	}

	if step.Assign != "" {
		result := map[string]any{
			"status":   "sent",
			"mode":     mode,
			"provider": smtpCfg.Provider,
			"from":     formatAddress(smtpCfg.FromName, smtpCfg.FromEmail),
			"to":       to,
			"accepted": len(to) + len(cc) + len(bcc),
		}
		if len(cc) > 0 {
			result["cc"] = cc
		}
		if len(bcc) > 0 {
			result["bcc"] = bcc
		}
		if replyTo != "" {
			result["reply_to"] = replyTo
		}
		if subject != "" {
			result["subject"] = subject
		}
		if templateID != "" {
			result["template_id"] = templateID
		}
		if sendMeta != nil && sendMeta.MessageID != "" {
			result["message_id"] = sendMeta.MessageID
		}
		pCtx.Set(step.Assign, result)
	}

	return nil
}

func validateEmailSendStep(step *tenant.FunctionStep) error {
	if step == nil {
		return errors.New("email.send: step is nil")
	}
	mode := strings.ToLower(strings.TrimSpace(step.Mode))
	if mode == "" {
		return errors.New("email.send: mode is required")
	}
	if mode != "raw" && mode != "template" {
		return fmt.Errorf("email.send: invalid mode %q", step.Mode)
	}
	if len(step.To) == 0 {
		return errors.New("email.send: to is required")
	}

	hasRecipient := false
	for _, rcpt := range step.To {
		if strings.TrimSpace(rcpt) != "" {
			hasRecipient = true
			break
		}
	}
	if !hasRecipient {
		return errors.New("email.send: to is required")
	}

	if mode == "template" && strings.TrimSpace(step.TemplateID) == "" {
		return errors.New("email.send: template_id is required in template mode")
	}
	if mode == "raw" && strings.TrimSpace(step.HTML) == "" && strings.TrimSpace(step.Text) == "" {
		return errors.New("email.send: html or text is required in raw mode")
	}

	return nil
}

func resolveRecipients(raw []string, pCtx *PipelineContext, field string, required bool) ([]string, error) {
	if len(raw) == 0 {
		if required {
			return nil, fmt.Errorf("%s is required", field)
		}
		return nil, nil
	}

	out := make([]string, 0, len(raw))
	for i, entry := range raw {
		resolved, err := ResolveString(entry, pCtx)
		if err != nil {
			return nil, fmt.Errorf("%s[%d] interpolation failed: %w", field, i, err)
		}

		values, err := flattenRecipientValues(resolved)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", field, i, err)
		}

		for _, candidate := range values {
			parsed, parseErr := mail.ParseAddress(candidate)
			if parseErr != nil {
				return nil, fmt.Errorf("%s[%d] invalid email %q: %w", field, i, candidate, parseErr)
			}
			out = append(out, parsed.Address)
		}
	}

	out = uniqueStrings(out)
	if required && len(out) == 0 {
		return nil, fmt.Errorf("%s is required", field)
	}
	return out, nil
}

func flattenRecipientValues(v any) ([]string, error) {
	switch val := v.(type) {
	case nil:
		return nil, nil
	case string:
		trimmed := strings.TrimSpace(val)
		if trimmed == "" {
			return nil, nil
		}
		return []string{trimmed}, nil
	case []string:
		out := make([]string, 0, len(val))
		for _, item := range val {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			out = append(out, trimmed)
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(val))
		for i, item := range val {
			itemValues, err := flattenRecipientValues(item)
			if err != nil {
				return nil, fmt.Errorf("recipient list index %d: %w", i, err)
			}
			out = append(out, itemValues...)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("recipient value must be string or array, got %T", v)
	}
}

func resolveTemplateData(raw map[string]any, pCtx *PipelineContext) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	return ResolveMap(raw, pCtx)
}

func resolveTenantTemplate(cfg *tenant.TenantConfig, templateID string) (tenant.EmailTemplate, error) {
	if cfg == nil {
		return tenant.EmailTemplate{}, errors.New("missing tenant config")
	}
	if strings.TrimSpace(templateID) == "" {
		return tenant.EmailTemplate{}, errors.New("template_id is required")
	}
	tpl, ok := cfg.APISettings.Email.Templates[templateID]
	if !ok {
		return tenant.EmailTemplate{}, fmt.Errorf("template_id %q not found", templateID)
	}
	return tpl, nil
}

func renderTenantTemplate(templateID string, tpl tenant.EmailTemplate, data map[string]any) (subject, htmlBody, textBody string, err error) {
	subject, err = renderTextTemplate("subject:"+templateID, tpl.Subject, data)
	if err != nil {
		return "", "", "", err
	}
	htmlBody, err = renderHTMLTemplate("html:"+templateID, tpl.HTML, data)
	if err != nil {
		return "", "", "", err
	}
	textBody, err = renderTextTemplate("text:"+templateID, tpl.Text, data)
	if err != nil {
		return "", "", "", err
	}
	return subject, htmlBody, textBody, nil
}

func renderTextTemplate(name, source string, data map[string]any) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", nil
	}
	tpl, err := texttemplate.New(name).Option("missingkey=error").Parse(source)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

func renderHTMLTemplate(name, source string, data map[string]any) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", nil
	}
	tpl, err := htmltemplate.New(name).Option("missingkey=error").Parse(source)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

func formatAddress(name, email string) string {
	if strings.TrimSpace(name) == "" {
		return strings.TrimSpace(email)
	}
	return (&mail.Address{Name: strings.TrimSpace(name), Address: strings.TrimSpace(email)}).String()
}

func logEmailSendFailure(deps *StepDeps, step *tenant.FunctionStep, err error) {
	tenantDB := ""
	if deps != nil && deps.Config != nil {
		tenantDB = deps.Config.DBName
	}

	functionID := ""
	functionName := ""
	if deps != nil {
		functionID = deps.FunctionID
		functionName = deps.FunctionName
	}

	stepType := "email.send"
	stepAssign := ""
	if step != nil {
		if step.Type != "" {
			stepType = step.Type
		}
		stepAssign = step.Assign
	}

	slog.Error("pipeline email.send failed",
		"tenant_db", tenantDB,
		"function_id", functionID,
		"function_name", functionName,
		"step_type", stepType,
		"step_assign", stepAssign,
		"error", err,
	)
}
