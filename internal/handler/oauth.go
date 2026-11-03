package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/oauth"
	"github.com/attisvalentin/supacraft/internal/pipeline"
	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

const (
	oauthStateTTL = 10 * time.Minute
	// A login code only travels from the callback redirect to the app's
	// exchange call.
	oauthCodeTTL = time.Minute
	// noPassword can't match any bcrypt hash: OAuth-only users have no
	// password until they reset one.
	noPassword = "!oauth"
)

// oauthSetup returns the provider of the request's {provider}, or writes
// 404 when OAuth or that provider isn't configured.
func oauthSetup(w http.ResponseWriter, r *http.Request) (*tenant.TenantConfig, string, tenant.OAuthProvider, bool) {
	cfg := middleware.TenantFromContext(r.Context())
	name := chi.URLParam(r, "provider")
	p, ok := cfg.APISettings.Auth.Provider(name)
	if !cfg.AuthEnabled || !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return nil, "", p, false
	}
	return cfg, name, p, true
}

func oauthProvider(ctx context.Context, cfg *tenant.TenantConfig, name string, p tenant.OAuthProvider) (*oauth.Provider, error) {
	callback := strings.TrimRight(cfg.APISettings.Auth.OAuth.PublicURL, "/") + "/auth/oauth/" + name + "/callback"
	// Provider URLs come from config, so the client that guards
	// http.request steps (and the sandbox in tests) applies.
	return oauth.New(ctx, pipeline.ClientFor(ctx, "https://provider"), name, p, callback)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// pkceChallenge is the S256 challenge of verifier (RFC 7636).
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

var challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// HandleOAuthStart: GET /auth/oauth/{provider}/start?redirect_to=<app url>
// &code_challenge=<S256 of an app-held verifier> (&tenant=<id> on a central
// server) sends the browser to the provider.
func HandleOAuthStart(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, name, p, ok := oauthSetup(w, r)
		if !ok {
			return
		}
		redirectTo := r.URL.Query().Get("redirect_to")
		if !cfg.APISettings.Auth.OAuth.RedirectAllowed(redirectTo) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "redirect_to is not one of api_settings.auth.oauth.redirect_urls"})
			return
		}
		// The app's own PKCE: only the browser that started can use the
		// resulting code, so a code from someone else's sign-in (login
		// CSRF) is worthless.
		appChallenge := r.URL.Query().Get("code_challenge")
		if !challengeRe.MatchString(appChallenge) || r.URL.Query().Get("code_challenge_method") != "S256" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code_challenge (S256 of a random code_verifier the app keeps) and code_challenge_method=S256 are required"})
			return
		}
		provider, err := oauthProvider(r.Context(), cfg, name, p)
		if err != nil {
			writeErrorJSON(w, r, http.StatusBadGateway, fmt.Sprintf("oauth %s: %v", name, err))
			return
		}
		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}
		// The tenant prefix routes the provider's callback on a central
		// server, where the browser can't send X-Tenant-ID.
		state := r.Header.Get("X-Tenant-ID") + "." + randomHex(32)
		verifier := oauth2.GenerateVerifier()
		now := time.Now()
		if _, err := dbPool.Exec(r.Context(), `DELETE FROM "_supacraft_oauth_states" WHERE "expires_at" < ?`, now.Unix()); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("oauth state: %v", err))
			return
		}
		if _, err := dbPool.Exec(r.Context(),
			`INSERT INTO "_supacraft_oauth_states" ("state_hash", "provider", "verifier", "app_challenge", "redirect_to", "expires_at") VALUES (?, ?, ?, ?, ?, ?)`,
			hashToken(state), name, verifier, appChallenge, redirectTo, now.Add(oauthStateTTL).Unix()); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("oauth state: %v", err))
			return
		}
		http.Redirect(w, r, provider.AuthURL(state, verifier), http.StatusFound)
	}
}

// HandleOAuthCallback: GET /auth/oauth/{provider}/callback?code&state — the
// provider sends the browser back here. The user is found or created and
// the browser goes on to redirect_to?code=<one-time login code> (or
// ?error=<reason>); the app exchanges the code at /auth/oauth/exchange.
func HandleOAuthCallback(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, name, p, ok := oauthSetup(w, r)
		if !ok {
			return
		}
		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}
		q := r.URL.Query()
		var provider, verifier, appChallenge, redirectTo string
		err = dbPool.QueryRow(r.Context(),
			`DELETE FROM "_supacraft_oauth_states" WHERE "state_hash" = ? AND "expires_at" > ? RETURNING "provider", "verifier", "app_challenge", "redirect_to"`,
			hashToken(q.Get("state")), time.Now().Unix()).Scan(&provider, &verifier, &appChallenge, &redirectTo)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && provider != name) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired sign-in; start again"})
			return
		}
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("oauth state: %v", err))
			return
		}
		back := func(key, value string) {
			u, _ := url.Parse(redirectTo)
			v := u.Query()
			v.Set(key, value)
			u.RawQuery = v.Encode()
			http.Redirect(w, r, u.String(), http.StatusFound)
		}
		if e := q.Get("error"); e != "" {
			back("error", "access_denied")
			return
		}
		prov, err := oauthProvider(r.Context(), cfg, name, p)
		var id oauth.Identity
		if err == nil {
			id, err = prov.Identity(r.Context(), q.Get("code"), verifier)
		}
		if err != nil {
			logError(r, http.StatusBadGateway, fmt.Sprintf("oauth %s: %v", name, err))
			back("error", "provider_error")
			return
		}
		userID, reason, err := oauthUser(r.Context(), dbPool, cfg, name, id)
		if err != nil {
			logError(r, http.StatusInternalServerError, fmt.Sprintf("oauth %s: %v", name, err))
			back("error", "server_error")
			return
		}
		if reason != "" {
			back("error", reason)
			return
		}
		code := randomHex(32)
		now := time.Now()
		if _, err := dbPool.Exec(r.Context(), `DELETE FROM "_supacraft_oauth_codes" WHERE "expires_at" < ?`, now.Unix()); err == nil {
			_, err = dbPool.Exec(r.Context(),
				`INSERT INTO "_supacraft_oauth_codes" ("code_hash", "user_id", "app_challenge", "expires_at") VALUES (?, ?, ?, ?)`,
				hashToken(code), userID, appChallenge, now.Add(oauthCodeTTL).Unix())
		}
		if err != nil {
			logError(r, http.StatusInternalServerError, fmt.Sprintf("oauth code: %v", err))
			back("error", "server_error")
			return
		}
		back("code", code)
	}
}

// oauthUser finds the user of a provider account, or — when the provider
// verified its email — links it to the user with that email or creates
// one. reason is a sign-in refusal to show the app (error=<reason>).
func oauthUser(ctx context.Context, db *tenant.DB, cfg *tenant.TenantConfig, provider string, id oauth.Identity) (userID, reason string, err error) {
	err = db.QueryRow(ctx, `SELECT "user_id" FROM "_supacraft_identities" WHERE "provider" = ? AND "subject" = ?`, provider, id.Subject).Scan(&userID)
	if err == nil {
		return userID, activeReason(ctx, db, userID), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if id.Email == "" {
		return "", "email_required", nil
	}
	// Only a verified email may sign in: an unverified one would let
	// someone claim another person's address at the provider, either
	// taking over their account or registering it before they do.
	if !id.EmailVerified {
		return "", "email_not_verified", nil
	}
	var localVerified bool
	err = db.QueryRow(ctx, `SELECT "id", COALESCE("email_verified", 0) FROM "users" WHERE "email" = ?`, id.Email).Scan(&userID, &localVerified)
	switch {
	case err == nil:
		if reason := activeReason(ctx, db, userID); reason != "" {
			return "", reason, nil
		}
		if !localVerified {
			if err := claimUnverified(ctx, db, cfg, userID); err != nil {
				return "", "", err
			}
		}
	case errors.Is(err, sql.ErrNoRows):
		if userID, err = createOAuthUser(ctx, db, cfg, id); err != nil {
			return "", "", err
		}
	default:
		return "", "", err
	}
	_, err = db.Exec(ctx, `INSERT INTO "_supacraft_identities" ("provider", "subject", "user_id", "email", "created_at") VALUES (?, ?, ?, ?, ?)`,
		provider, id.Subject, userID, id.Email, time.Now().Unix())
	return userID, "", err
}

// claimUnverified hands a local account whose email was never verified to
// the provider account that just proved owning the email: anyone else who
// registered it (account pre-hijacking) loses the password and every
// session; the owner can reset a password later.
func claimUnverified(ctx context.Context, db *tenant.DB, cfg *tenant.TenantConfig, userID string) error {
	_, err := tenant.WriteWithWebhooks(ctx, db, cfg, "users", tenant.OpUpdate, func(q tenant.Querier) ([]map[string]any, error) {
		return queryRows(ctx, q, cfg, `UPDATE "users" SET "password" = ?, "email_verified" = 1, "session_version" = COALESCE("session_version", 0) + 1, "updated_at" = datetime('now') WHERE "id" = ? RETURNING *`, noPassword, userID)
	})
	return err
}

func activeReason(ctx context.Context, db *tenant.DB, userID string) string {
	var active bool
	if err := db.QueryRow(ctx, `SELECT COALESCE("is_active", 1) FROM "users" WHERE "id" = ?`, userID).Scan(&active); err != nil || !active {
		return "account_disabled"
	}
	return ""
}

// createOAuthUser adds a user for a new provider account, like
// /auth/register does (default role, users.insert webhooks), without a
// usable password.
func createOAuthUser(ctx context.Context, db *tenant.DB, cfg *tenant.TenantConfig, id oauth.Identity) (string, error) {
	table, err := schema.FindTableByName(&cfg.Schema, "users")
	if err != nil {
		return "", err
	}
	row := map[string]any{"id": uuid.NewString(), "email": id.Email, "password": noPassword, "email_verified": true}
	if role := cfg.APISettings.Auth.DefaultRoleName(); role != "" {
		row["role"] = role
	}
	insertSQL, args := query.BuildInsert(table.Name, row)
	if _, err := insertRow(ctx, db, cfg, table, insertSQL, args); err != nil {
		return "", err
	}
	return row["id"].(string), nil
}

// HandleOAuthExchange: POST /auth/oauth/exchange {"code", "code_verifier"}
// turns the login code from the callback redirect into the tokens
// /auth/login returns; code_verifier must match the start's challenge.
func HandleOAuthExchange(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())
		if !cfg.AuthEnabled || cfg.APISettings.Auth.OAuth == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		var body struct {
			Code         string `json:"code"`
			CodeVerifier string `json:"code_verifier"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" || body.CodeVerifier == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"code\", \"code_verifier\"}"})
			return
		}
		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}
		var userID, appChallenge string
		err = dbPool.QueryRow(r.Context(),
			`DELETE FROM "_supacraft_oauth_codes" WHERE "code_hash" = ? AND "expires_at" > ? RETURNING "user_id", "app_challenge"`,
			hashToken(body.Code), time.Now().Unix()).Scan(&userID, &appChallenge)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && subtle.ConstantTimeCompare([]byte(pkceChallenge(body.CodeVerifier)), []byte(appChallenge)) != 1) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired code"})
			return
		}
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("oauth exchange: %v", err))
			return
		}
		var email string
		var active, verified bool
		var version int64
		err = dbPool.QueryRow(r.Context(),
			`SELECT "email", COALESCE("is_active", 1), COALESCE("email_verified", 0), COALESCE("session_version", 0) FROM "users" WHERE "id" = ?`, userID,
		).Scan(&email, &active, &verified, &version)
		if err != nil || !active {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
			return
		}
		if v := cfg.APISettings.Auth.EmailVerification; v.Enabled() && v.Required && !verified {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "email not verified"})
			return
		}
		writeTokens(w, r, dbPool, cfg, userID, email, version)
	}
}
