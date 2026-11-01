package tenant

import (
	"bytes"
	"encoding/json"
	"time"
)

// EnvMap is a string→string map that tolerates being serialized as a JSON
// array instead of an object. Hand-written and generated configs may use `[]` for empty
// associative arrays, and some producers emit a plain list of variable names
// (`["FOO", "BAR"]`) rather than name→value pairs. Either array form would
// otherwise fail to unmarshal into a Go map and abort parsing the whole tenant
// config. An array of names maps each entry to an empty string value.
type EnvMap map[string]string

func (m *EnvMap) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*m = EnvMap{}
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var names []string
		if err := json.Unmarshal(trimmed, &names); err != nil {
			return err
		}
		out := make(EnvMap, len(names))
		for _, name := range names {
			out[name] = ""
		}
		*m = out
		return nil
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = raw
	return nil
}

type Schema struct {
	TenantID  string        `json:"tenant_id"`
	Tables    []SchemaTable `json:"tables"`
	Relations []Relation    `json:"relations"`
}

type SchemaTable struct {
	UUID    string         `json:"uuid"`
	Name    string         `json:"name"`
	Columns []SchemaColumn `json:"columns"`
}

type SchemaColumn struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Readable  *bool  `json:"readable,omitempty"`
	Creatable *bool  `json:"creatable,omitempty"`
	Updatable *bool  `json:"updatable,omitempty"`
}

func (c *SchemaColumn) IsReadable() bool  { return c.Readable == nil || *c.Readable }
func (c *SchemaColumn) IsCreatable() bool { return c.Creatable == nil || *c.Creatable }
func (c *SchemaColumn) IsUpdatable() bool { return c.Updatable == nil || *c.Updatable }

type AuthClaims struct {
	UserID string
	Email  string
}

type Relation struct {
	From RelationData `json:"from"`
	To   RelationData `json:"to"`
	Type string       `json:"type"`
}

type RelationData struct {
	Table  string `json:"table"`
	Column string `json:"column"`
}

type AuthEndpoints struct {
	Register *bool `json:"register,omitempty"`
	Login    *bool `json:"login,omitempty"`
	Refresh  *bool `json:"refresh,omitempty"`
}

func (a AuthEndpoints) RegisterEnabled() bool {
	if a.Register == nil {
		return true
	}
	return *a.Register
}

func (a AuthEndpoints) LoginEnabled() bool {
	if a.Login == nil {
		return true
	}
	return *a.Login
}

func (a AuthEndpoints) RefreshEnabled() bool {
	if a.Refresh == nil {
		return true
	}
	return *a.Refresh
}

// AuthSettings holds configurable JWT lifetimes (seconds). nil/≤0 values
// fall back to the historical defaults so existing tenants are unaffected.
type AuthSettings struct {
	AccessTokenTTL    *int `json:"access_token_ttl,omitempty"`
	RefreshTokenTTL   *int `json:"refresh_token_ttl,omitempty"`
	PasswordMinLength *int `json:"password_min_length,omitempty"`
	// PasswordReset enables /auth/forgot-password and /auth/reset-password;
	// EmailVerification enables /auth/verify-email and
	// /auth/resend-verification and mails a link on register.
	PasswordReset     *AuthEmailFlow `json:"password_reset,omitempty"`
	EmailVerification *AuthEmailFlow `json:"email_verification,omitempty"`
}

// AuthEmailFlow mails a one-time link: Template (from email.templates)
// gets {{.link}}, {{.token}}, {{.email}} and {{.expires_in_minutes}}; URL
// is the app page that takes the token, with a "{token}" placeholder.
type AuthEmailFlow struct {
	Template string `json:"template"`
	URL      string `json:"url"`
	TokenTTL int    `json:"token_ttl,omitempty"` // seconds
	// Required (email_verification only) refuses login until the email
	// is verified.
	Required bool `json:"required,omitempty"`
}

// Enabled reports whether the flow is configured.
func (f *AuthEmailFlow) Enabled() bool { return f != nil && f.Template != "" && f.URL != "" }

// TTL returns the token lifetime, def when unset.
func (f *AuthEmailFlow) TTL(def time.Duration) time.Duration {
	if f == nil || f.TokenTTL <= 0 {
		return def
	}
	return time.Duration(f.TokenTTL) * time.Second
}

const defaultPasswordMinLength = 8

// MaxPasswordBytes is bcrypt's input limit; longer passwords are rejected.
const MaxPasswordBytes = 72

// MinPasswordLength returns the minimum password length for registration.
func (a AuthSettings) MinPasswordLength() int {
	if a.PasswordMinLength == nil || *a.PasswordMinLength <= 0 {
		return defaultPasswordMinLength
	}
	return *a.PasswordMinLength
}

const (
	defaultAccessTTLSeconds  = 86400   // 24 hours
	defaultRefreshTTLSeconds = 2592000 // 30 days
)

// AccessTTLSeconds returns the access-token lifetime in seconds.
func (a AuthSettings) AccessTTLSeconds() int {
	if a.AccessTokenTTL == nil || *a.AccessTokenTTL <= 0 {
		return defaultAccessTTLSeconds
	}
	return *a.AccessTokenTTL
}

// RefreshTTLSeconds returns the refresh-token lifetime in seconds.
func (a AuthSettings) RefreshTTLSeconds() int {
	if a.RefreshTokenTTL == nil || *a.RefreshTokenTTL <= 0 {
		return defaultRefreshTTLSeconds
	}
	return *a.RefreshTokenTTL
}

// AccessTTL returns the access-token lifetime as a duration.
func (a AuthSettings) AccessTTL() time.Duration {
	return time.Duration(a.AccessTTLSeconds()) * time.Second
}

// RefreshTTL returns the refresh-token lifetime as a duration.
func (a AuthSettings) RefreshTTL() time.Duration {
	return time.Duration(a.RefreshTTLSeconds()) * time.Second
}

type CRUDEndpoints struct {
	Query  *bool `json:"query,omitempty"`
	Create *bool `json:"create,omitempty"`
	Update *bool `json:"update,omitempty"`
	Delete *bool `json:"delete,omitempty"`
}

func (c CRUDEndpoints) QueryEnabled() bool {
	if c.Query == nil {
		return true
	}
	return *c.Query
}

func (c CRUDEndpoints) CreateEnabled() bool {
	if c.Create == nil {
		return true
	}
	return *c.Create
}

func (c CRUDEndpoints) UpdateEnabled() bool {
	if c.Update == nil {
		return true
	}
	return *c.Update
}

func (c CRUDEndpoints) DeleteEnabled() bool {
	if c.Delete == nil {
		return true
	}
	return *c.Delete
}

type APISettings struct {
	Tables        map[string]TableMethodSettings `json:"tables"`
	Auth          AuthSettings                   `json:"auth,omitempty"`
	AuthEndpoints AuthEndpoints                  `json:"auth_endpoints,omitempty"`
	CRUDEndpoints CRUDEndpoints                  `json:"crud_endpoints,omitempty"`
	FileEndpoints FileEndpoints                  `json:"file_endpoints,omitempty"`
	Env           EnvMap                         `json:"env,omitempty"`
	Email         EmailSettings                  `json:"email,omitempty"`
	Storage       *StorageConfig                 `json:"storage,omitempty"`
	Timezone      string                         `json:"timezone,omitempty"`
	RateLimit     RateLimitSettings              `json:"rate_limit,omitempty"`
	CORS          CORSSettings                   `json:"cors,omitempty"`
}

// CORSSettings controls the Access-Control-Allow-Origin header returned to
// browser-based frontends. AllowedOrigin is either "*" (any origin) or one
// specific origin (scheme + host[:port], no path). Empty means no CORS
// header is sent.
type CORSSettings struct {
	AllowedOrigin string `json:"allowed_origin,omitempty"`
}

type RateLimitSettings struct {
	Enabled           bool `json:"enabled"`
	RequestsPerMinute int  `json:"requests_per_minute,omitempty"`
}

// LoadTimezone parses an IANA timezone name and returns a *time.Location.
// Returns time.UTC when tz is empty, "UTC", or unrecognised.
func LoadTimezone(tz string) *time.Location {
	if tz == "" || tz == "UTC" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

type EmailSettings struct {
	SMTP      SMTPSettings             `json:"smtp,omitempty"`
	Templates map[string]EmailTemplate `json:"templates,omitempty"`
}

type SMTPSettings struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	Encryption string `json:"encryption,omitempty"`
	FromEmail  string `json:"from_email,omitempty"`
	FromName   string `json:"from_name,omitempty"`
}

type EmailTemplate struct {
	Subject string `json:"subject,omitempty"`
	HTML    string `json:"html,omitempty"`
	Text    string `json:"text,omitempty"`
}

type TableMethodSettings struct {
	Query  MethodSettings `json:"query"`
	Create MethodSettings `json:"create"`
	Update MethodSettings `json:"update"`
	Delete MethodSettings `json:"delete"`
}

type MethodSettings struct {
	Enabled   bool   `json:"enabled"`
	Auth      bool   `json:"auth"`
	RowPolicy string `json:"row_policy,omitempty"`
}

type TenantConfig struct {
	DBName      string              `json:"db_name"`
	AuthEnabled bool                `json:"auth_enabled"`
	Enabled     bool                `json:"enabled"`
	Schema      Schema              `json:"schema"`
	APISettings APISettings         `json:"api_settings"`
	Functions   map[string]Function `json:"functions,omitempty"`
	Cronjobs    CronjobMap          `json:"cronjobs,omitempty"`
	DisplayName string              `json:"display_name"`
	UpdatedAt   string              `json:"updated_at"`
}
