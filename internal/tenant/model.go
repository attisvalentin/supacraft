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
	// Descriptive properties, used by the OpenAPI export.
	Primary      bool     `json:"primary,omitempty"`
	Nullable     *bool    `json:"nullable,omitempty"`
	DefaultValue any      `json:"default_value,omitempty"`
	EnumValues   []string `json:"enum_values,omitempty"`
}

// IsNullable reports whether the column accepts NULL (the default).
func (c *SchemaColumn) IsNullable() bool { return c.Nullable == nil || *c.Nullable }

func (c *SchemaColumn) IsReadable() bool  { return c.Readable == nil || *c.Readable }
func (c *SchemaColumn) IsCreatable() bool { return c.Creatable == nil || *c.Creatable }
func (c *SchemaColumn) IsUpdatable() bool { return c.Updatable == nil || *c.Updatable }

type AuthClaims struct {
	UserID string
	Email  string
	// Role is read from the users table on every request, so a role
	// change applies to tokens already issued. "" when roles are off.
	Role string
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
	// Roles lists the roles a user can have; routes restrict access with
	// their own "roles". New users get DefaultRole (the first role when
	// unset). Empty disables roles.
	Roles       []string `json:"roles,omitempty"`
	DefaultRole string   `json:"default_role,omitempty"`
	// OAuth signs users in with Google, GitHub or any OpenID Connect
	// provider.
	OAuth *OAuthSettings `json:"oauth,omitempty"`
}

// OAuthSettings configures sign-in through external providers. PublicURL
// is the API's public base URL (the providers call back to
// <PublicURL>/auth/oauth/<provider>/callback); RedirectURLs are the app
// pages a sign-in may return to.
type OAuthSettings struct {
	PublicURL    string                   `json:"public_url"`
	RedirectURLs []string                 `json:"redirect_urls"`
	Providers    map[string]OAuthProvider `json:"providers"`
}

// OAuthProvider is one provider: "google" and "github" are built in; any
// other name is an OpenID Connect provider found from Issuer.
type OAuthProvider struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Issuer       string   `json:"issuer,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// Provider returns the configured provider name, if OAuth is set up.
func (a AuthSettings) Provider(name string) (OAuthProvider, bool) {
	if a.OAuth == nil {
		return OAuthProvider{}, false
	}
	p, ok := a.OAuth.Providers[name]
	return p, ok
}

// RedirectAllowed reports whether a sign-in may return to url: it must be
// one of RedirectURLs exactly, so nobody can send the login code elsewhere.
func (o *OAuthSettings) RedirectAllowed(url string) bool {
	if o == nil {
		return false
	}
	for _, u := range o.RedirectURLs {
		if u == url {
			return true
		}
	}
	return false
}

// RolesEnabled reports whether the project defines roles.
func (a AuthSettings) RolesEnabled() bool { return len(a.Roles) > 0 }

// DefaultRoleName is the role of a new user (and of one whose role is
// NULL); "" when roles are disabled.
func (a AuthSettings) DefaultRoleName() string {
	if a.DefaultRole != "" {
		return a.DefaultRole
	}
	if len(a.Roles) > 0 {
		return a.Roles[0]
	}
	return ""
}

// HasRole reports whether role is one of the configured roles.
func (a AuthSettings) HasRole(role string) bool {
	for _, r := range a.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// EffectiveRole maps a stored users.role value to the user's role: NULL
// means the default role, and a role no longer configured means none.
func (a AuthSettings) EffectiveRole(stored *string) string {
	if !a.RolesEnabled() {
		return ""
	}
	if stored == nil || *stored == "" {
		return a.DefaultRoleName()
	}
	if a.HasRole(*stored) {
		return *stored
	}
	return ""
}

// RoleAllowed reports whether role may use a route restricted to allowed
// (an empty list allows everyone).
func RoleAllowed(allowed []string, role string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, r := range allowed {
		if r == role && role != "" {
			return true
		}
	}
	return false
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
	// OpenAPI serves GET /meta/openapi.json; on unless set to false.
	OpenAPI *bool `json:"openapi,omitempty"`
	// Webhooks POST table changes to other systems, by name.
	Webhooks map[string]Webhook `json:"webhooks,omitempty"`
}

// OpenAPIEnabled reports whether GET /meta/openapi.json is served.
func (a APISettings) OpenAPIEnabled() bool { return a.OpenAPI == nil || *a.OpenAPI }

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
	// Roles limits the method to users with one of these roles (implies
	// auth).
	Roles []string `json:"roles,omitempty"`
}

// NeedsAuth reports whether the method requires a signed-in user.
func (m MethodSettings) NeedsAuth() bool { return m.Auth || len(m.Roles) > 0 }

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
