package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// dummyHash is compared against when a login email is unknown.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("supacraft-timing-equalizer"), bcrypt.DefaultCost)

type AuthRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func HandleRegister(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		if !cfg.AuthEnabled {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if !cfg.APISettings.AuthEndpoints.RegisterEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		emailVal, ok := body["email"]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
			return
		}
		email, ok := emailVal.(string)
		if !ok || email == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email must be a non-empty string"})
			return
		}

		passwordVal, ok := body["password"]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password is required"})
			return
		}
		password, _ := passwordVal.(string)
		if msg := checkPassword(cfg, password); msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}

		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to hash password: %v", err))
			return
		}
		body["password"] = string(hashedPassword)

		// id is server-generated, never client-controlled. Drop any supplied
		// value before validation so it can't be spoofed or rejected.
		delete(body, "id")

		table, err := schema.FindTableByName(&cfg.Schema, "users")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "users table not configured in schema"})
			return
		}

		columns := make([]string, 0, len(body))
		for col := range body {
			columns = append(columns, col)
		}
		if err := schema.ValidateCreateColumns(columns, table); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}

		// Assign the primary key after validation (server-controlled).
		body["id"] = uuid.NewString()

		insertSQL, args := query.BuildInsert(table.Name, body)
		res, err := dbPool.Exec(r.Context(), insertSQL, args...)
		if err != nil {
			writeErrorJSON(w, r, http.StatusBadRequest, fmt.Sprintf("registration failed: %v", err))
			return
		}

		row, err := readInsertedRow(r, dbPool, table, res, cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		if row == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no result returned"})
			return
		}
		sendVerificationOnRegister(r, dbPool, cfg, email)
		writeJSON(w, http.StatusCreated, map[string]any{"data": row})
	}
}

func HandleLogin(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		if !cfg.AuthEnabled {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if !cfg.APISettings.AuthEndpoints.LoginEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		var req AuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}

		var userID, hashedPassword string
		var active, verified bool
		var version int64
		err = dbPool.QueryRow(r.Context(),
			`SELECT "id", "password", COALESCE("is_active", 1), COALESCE("email_verified", 0), COALESCE("session_version", 0) FROM "users" WHERE "email" = ?`,
			req.Email,
		).Scan(&userID, &hashedPassword, &active, &verified, &version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("login query: %v", err))
			return
		}
		if err != nil {
			// Spend the same bcrypt time as a real check, so response timing
			// doesn't reveal which emails are registered.
			bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(req.Password)); err != nil || !active {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
			return
		}
		// Checked after the password, so it reveals nothing to a guesser.
		if v := cfg.APISettings.Auth.EmailVerification; v.Enabled() && v.Required && !verified {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "email not verified"})
			return
		}

		var jwtSecret string
		err = dbPool.QueryRow(r.Context(),
			`SELECT "jwt_secret" FROM "api_config" LIMIT 1`,
		).Scan(&jwtSecret)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("auth configuration missing: %v", err))
			return
		}

		auth := cfg.APISettings.Auth
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub":   userID,
			"email": req.Email,
			"exp":   time.Now().Add(auth.AccessTTL()).Unix(),
			"iat":   time.Now().Unix(),
			"ver":   version,
		})

		tokenString, err := token.SignedString([]byte(jwtSecret))
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to create token: %v", err))
			return
		}

		refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub":   userID,
			"email": req.Email,
			"type":  "refresh",
			"exp":   time.Now().Add(auth.RefreshTTL()).Unix(),
			"iat":   time.Now().Unix(),
			"ver":   version,
		})

		refreshTokenString, err := refreshToken.SignedString([]byte(jwtSecret))
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to create refresh token: %v", err))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"token":         tokenString,
				"refresh_token": refreshTokenString,
				"expires_in":    auth.AccessTTLSeconds(),
			},
		})
	}
}

func HandleRefresh(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		if !cfg.AuthEnabled {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if !cfg.APISettings.AuthEndpoints.RefreshEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		var req RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}

		var jwtSecret string
		err = dbPool.QueryRow(r.Context(),
			`SELECT "jwt_secret" FROM "api_config" LIMIT 1`,
		).Scan(&jwtSecret)

		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("auth configuration missing: %v", err))
			return
		}

		token, err := jwt.Parse(req.RefreshToken, func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return []byte(jwtSecret), nil
		})
		if err != nil || !token.Valid {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid refresh token"})
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token claims"})
			return
		}
		if claims["type"] != "refresh" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not a refresh token"})
			return
		}

		userID, ok := claims["sub"].(string)
		if !ok || userID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token subject"})
			return
		}
		email, _ := claims["email"].(string)

		// Refresh tokens outlive account changes: re-check that the user
		// still exists, is active and hasn't reset the password since.
		version, ok := sessionVersion(r.Context(), dbPool, userID)
		if claimed, _ := claims["ver"].(float64); !ok || int64(claimed) != version {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid refresh token"})
			return
		}

		auth := cfg.APISettings.Auth
		newToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub":   userID,
			"email": email,
			"exp":   time.Now().Add(auth.AccessTTL()).Unix(),
			"iat":   time.Now().Unix(),
			"ver":   version,
		})
		newTokenString, err := newToken.SignedString([]byte(jwtSecret))
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to create token: %v", err))
			return
		}

		newRefreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub":   userID,
			"email": email,
			"type":  "refresh",
			"exp":   time.Now().Add(auth.RefreshTTL()).Unix(),
			"iat":   time.Now().Unix(),
			"ver":   version,
		})
		newRefreshTokenString, err := newRefreshToken.SignedString([]byte(jwtSecret))
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("failed to create refresh token: %v", err))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"token":         newTokenString,
				"refresh_token": newRefreshTokenString,
				"expires_in":    auth.AccessTTLSeconds(),
			},
		})
	}
}

func validateJWT(r *http.Request, cfg *tenant.TenantConfig, pool *tenant.PoolManager) (*tenant.AuthClaims, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, fmt.Errorf("missing authorization header")
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return nil, fmt.Errorf("invalid authorization header format")
	}

	tokenString := parts[1]

	dbPool, err := pool.GetPool(r.Context(), cfg)
	if err != nil {
		return nil, fmt.Errorf("database connection failed")
	}

	var jwtSecret string
	err = dbPool.QueryRow(r.Context(),
		`SELECT "jwt_secret" FROM "api_config" LIMIT 1`,
	).Scan(&jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("auth configuration missing")
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(jwtSecret), nil
	})

	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}

	if claims["type"] == "refresh" {
		return nil, fmt.Errorf("refresh token cannot be used as access token")
	}

	userID, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	if !sessionValid(r.Context(), dbPool, userID, claims["ver"]) {
		return nil, fmt.Errorf("session ended")
	}

	return &tenant.AuthClaims{
		UserID: userID,
		Email:  email,
	}, nil
}

// sessionVersion returns the user's session version if the user exists
// and is active; ok is false otherwise.
func sessionVersion(ctx context.Context, db *tenant.DB, userID string) (version int64, ok bool) {
	if userID == "" {
		return 0, false
	}
	var active bool
	err := db.QueryRow(ctx,
		`SELECT COALESCE("is_active", 1), COALESCE("session_version", 0) FROM "users" WHERE "id" = ?`, userID,
	).Scan(&active, &version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("session check failed", "error", err)
	}
	return version, err == nil && active
}

// sessionValid reports whether a token for userID carrying ver (its "ver"
// claim; absent counts as 0) is still good: the user exists, is active,
// and hasn't reset the password since it was issued.
func sessionValid(ctx context.Context, db *tenant.DB, userID string, ver any) bool {
	current, ok := sessionVersion(ctx, db, userID)
	claimed, _ := ver.(float64)
	return ok && int64(claimed) == current
}
