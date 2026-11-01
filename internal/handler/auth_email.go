package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/pipeline"
	"github.com/attisvalentin/supacraft/internal/tenant"

	"golang.org/x/crypto/bcrypt"
)

// Purposes of the one-time tokens in _supacraft_auth_tokens.
const (
	purposeReset  = "password_reset"
	purposeVerify = "email_verification"
)

const (
	defaultResetTTL  = time.Hour
	defaultVerifyTTL = 24 * time.Hour
	// authEmailInterval is the shortest time between two auth emails of
	// one purpose to one user, so the endpoints can't flood a mailbox.
	authEmailInterval = time.Minute
	authEmailTimeout  = 30 * time.Second
)

// authMailer and runInBackground are replaced in tests.
var (
	authMailer      pipeline.Mailer = pipeline.NewSMTPMailer()
	runInBackground                 = func(f func()) { go f() }
)

// acceptedResponse is the answer of forgot-password and resend-verification
// whether or not the email belongs to a user, so they can't be used to find
// registered emails. The email itself is sent in the background for the
// same reason: its timing would tell.
func acceptedResponse(w http.ResponseWriter) {
	writeJSON(w, http.StatusAccepted, map[string]any{
		"data": map[string]any{"message": "if the email belongs to an account, a link has been sent"},
	})
}

// HandleForgotPassword: POST /auth/forgot-password {"email"}
func HandleForgotPassword(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())
		flow := cfg.APISettings.Auth.PasswordReset
		if !cfg.AuthEnabled || !flow.Enabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		email, ok := decodeEmail(w, r)
		if !ok {
			return
		}
		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}
		tenantID := r.Header.Get("X-Tenant-ID")
		runInBackground(func() {
			mailAuthLink(dbPool, cfg, tenantID, r.URL.Path, flow, purposeReset, defaultResetTTL, email, false)
		})
		acceptedResponse(w)
	}
}

// HandleResendVerification: POST /auth/resend-verification {"email"}
func HandleResendVerification(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())
		flow := cfg.APISettings.Auth.EmailVerification
		if !cfg.AuthEnabled || !flow.Enabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		email, ok := decodeEmail(w, r)
		if !ok {
			return
		}
		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}
		tenantID := r.Header.Get("X-Tenant-ID")
		runInBackground(func() {
			mailAuthLink(dbPool, cfg, tenantID, r.URL.Path, flow, purposeVerify, defaultVerifyTTL, email, true)
		})
		acceptedResponse(w)
	}
}

// sendVerificationOnRegister mails the verification link to a new user.
func sendVerificationOnRegister(r *http.Request, dbPool *tenant.DB, cfg *tenant.TenantConfig, email string) {
	flow := cfg.APISettings.Auth.EmailVerification
	if !flow.Enabled() {
		return
	}
	tenantID := r.Header.Get("X-Tenant-ID")
	runInBackground(func() {
		mailAuthLink(dbPool, cfg, tenantID, r.URL.Path, flow, purposeVerify, defaultVerifyTTL, email, true)
	})
}

// HandleResetPassword: POST /auth/reset-password {"token", "password"}
func HandleResetPassword(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())
		if !cfg.AuthEnabled || !cfg.APISettings.Auth.PasswordReset.Enabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		var req struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"token": "...", "password": "..."}`})
			return
		}
		if msg := checkPassword(cfg, req.Password); msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
		hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to hash password: %v", err))
			return
		}
		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}
		tx, err := dbPool.Begin(r.Context())
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("begin: %v", err))
			return
		}
		defer tx.Rollback()
		userID, err := consumeToken(r.Context(), tx, req.Token, purposeReset)
		if errors.Is(err, errBadToken) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired token"})
			return
		}
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("reset password: %v", err))
			return
		}
		// The link reached the mailbox, so the email is verified too. A new
		// session_version ends every session issued before.
		if _, err := tx.Exec(r.Context(),
			`UPDATE "users" SET "password" = ?, "session_version" = COALESCE("session_version", 0) + 1, "email_verified" = 1, "updated_at" = datetime('now') WHERE "id" = ?`,
			string(hashed), userID); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("reset password: %v", err))
			return
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM "_supacraft_auth_tokens" WHERE "user_id" = ? AND "purpose" = ?`, userID, purposeReset); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("reset password: %v", err))
			return
		}
		if err := tx.Commit(); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("reset password: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"password_reset": true}})
	}
}

// HandleVerifyEmail: POST /auth/verify-email {"token"}
func HandleVerifyEmail(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())
		if !cfg.AuthEnabled || !cfg.APISettings.Auth.EmailVerification.Enabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		var req struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"token": "..."}`})
			return
		}
		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}
		tx, err := dbPool.Begin(r.Context())
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("begin: %v", err))
			return
		}
		defer tx.Rollback()
		userID, err := consumeToken(r.Context(), tx, req.Token, purposeVerify)
		if errors.Is(err, errBadToken) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired token"})
			return
		}
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("verify email: %v", err))
			return
		}
		if _, err := tx.Exec(r.Context(), `UPDATE "users" SET "email_verified" = 1, "updated_at" = datetime('now') WHERE "id" = ?`, userID); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("verify email: %v", err))
			return
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM "_supacraft_auth_tokens" WHERE "user_id" = ? AND "purpose" = ?`, userID, purposeVerify); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("verify email: %v", err))
			return
		}
		if err := tx.Commit(); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("verify email: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"email_verified": true}})
	}
}

var errBadToken = errors.New("invalid or expired token")

// consumeToken deletes a valid token and returns its user, so each link
// works once.
func consumeToken(ctx context.Context, q tenant.Querier, token, purpose string) (string, error) {
	var userID string
	err := q.QueryRow(ctx,
		`DELETE FROM "_supacraft_auth_tokens" WHERE "token_hash" = ? AND "purpose" = ? AND "expires_at" > ? RETURNING "user_id"`,
		hashToken(token), purpose, time.Now().Unix()).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errBadToken
	}
	return userID, err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// mailAuthLink creates a token for the active user with this email and
// mails the link. It runs in the background: failures go to the error log
// (supacraft tenants errors), never to the client.
func mailAuthLink(db *tenant.DB, cfg *tenant.TenantConfig, tenantID, path string, flow *tenant.AuthEmailFlow, purpose string, defTTL time.Duration, email string, onlyUnverified bool) {
	ctx, cancel := context.WithTimeout(context.Background(), authEmailTimeout)
	defer cancel()
	fail := func(err error) {
		msg := fmt.Sprintf("%s email: %v", purpose, err)
		if errorlog.Global != nil {
			errorlog.Global.Log(tenantID, http.MethodPost, path, http.StatusInternalServerError, msg)
		} else {
			slog.Error(msg, "tenant", tenantID)
		}
	}

	var userID string
	var verified bool
	err := db.QueryRow(ctx,
		`SELECT "id", COALESCE("email_verified", 0) FROM "users" WHERE "email" = ? AND COALESCE("is_active", 1)`,
		email).Scan(&userID, &verified)
	if errors.Is(err, sql.ErrNoRows) || (onlyUnverified && verified) {
		return
	}
	if err != nil {
		fail(err)
		return
	}

	now := time.Now()
	var recent int
	if err := db.QueryRow(ctx,
		`SELECT COUNT(*) FROM "_supacraft_auth_tokens" WHERE "user_id" = ? AND "purpose" = ? AND "created_at" > ?`,
		userID, purpose, now.Add(-authEmailInterval).Unix()).Scan(&recent); err != nil {
		fail(err)
		return
	}
	if recent > 0 {
		return
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		fail(err)
		return
	}
	token := hex.EncodeToString(raw)
	ttl := flow.TTL(defTTL)
	// Only the newest link of a purpose works.
	if _, err := db.Exec(ctx, `DELETE FROM "_supacraft_auth_tokens" WHERE "user_id" = ? AND "purpose" = ?`, userID, purpose); err != nil {
		fail(err)
		return
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO "_supacraft_auth_tokens" ("token_hash", "user_id", "purpose", "expires_at", "created_at") VALUES (?, ?, ?, ?, ?)`,
		hashToken(token), userID, purpose, now.Add(ttl).Unix(), now.Unix()); err != nil {
		fail(err)
		return
	}

	link := strings.ReplaceAll(flow.URL, "{token}", url.QueryEscape(token))
	subject, htmlBody, textBody, err := pipeline.RenderEmailTemplate(cfg, flow.Template, map[string]any{
		"link":               link,
		"token":              token,
		"email":              email,
		"expires_in_minutes": int(ttl.Minutes()),
	})
	if err != nil {
		fail(err)
		return
	}
	smtpCfg := cfg.APISettings.Email.SMTP
	if _, err := authMailer.Send(ctx, smtpCfg, pipeline.MailMessage{
		FromEmail: smtpCfg.FromEmail,
		FromName:  smtpCfg.FromName,
		To:        []string{email},
		Subject:   subject,
		HTMLBody:  htmlBody,
		TextBody:  textBody,
	}); err != nil {
		fail(err)
	}
}

// decodeEmail reads {"email": "..."} or writes a 400.
func decodeEmail(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"email": "..."}`})
		return "", false
	}
	return req.Email, true
}

// checkPassword applies the tenant's password rules; "" means valid.
func checkPassword(cfg *tenant.TenantConfig, password string) string {
	if password == "" {
		return "password must be a non-empty string"
	}
	if minLen := cfg.APISettings.Auth.MinPasswordLength(); utf8.RuneCountInString(password) < minLen {
		return fmt.Sprintf("password must be at least %d characters", minLen)
	}
	// bcrypt ignores everything past 72 bytes.
	if len(password) > tenant.MaxPasswordBytes {
		return fmt.Sprintf("password must be at most %d bytes", tenant.MaxPasswordBytes)
	}
	return ""
}
