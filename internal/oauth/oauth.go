// Package oauth talks to the sign-in providers: Google and any OpenID
// Connect provider (found through discovery) and GitHub (its own API). It
// builds the authorization URL and turns a callback's code into the
// provider's account: a stable subject id and the email, if verified.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/attisvalentin/supacraft/internal/tenant"
	"golang.org/x/oauth2"
)

// Provider endpoints; tests point them at fakes.
var (
	GoogleIssuer   = "https://accounts.google.com"
	GitHubAuthURL  = "https://github.com/login/oauth/authorize"
	GitHubTokenURL = "https://github.com/login/oauth/access_token"
	GitHubAPI      = "https://api.github.com"
)

const maxResponse = 1 << 20

// Identity is the account a provider vouches for.
type Identity struct {
	Subject       string // stable id at the provider
	Email         string
	EmailVerified bool
}

// Provider is a configured provider ready for one sign-in.
type Provider struct {
	config   oauth2.Config
	client   *http.Client
	identity func(ctx context.Context, p *Provider, tok *oauth2.Token) (Identity, error)
	userinfo string
}

// New prepares the provider name of cfg; callback is its redirect URI.
// client makes every request to the provider.
func New(ctx context.Context, client *http.Client, name string, cfg tenant.OAuthProvider, callback string) (*Provider, error) {
	p := &Provider{client: client, config: oauth2.Config{
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: callback, Scopes: cfg.Scopes,
	}}
	if name == "github" {
		p.config.Endpoint = oauth2.Endpoint{AuthURL: GitHubAuthURL, TokenURL: GitHubTokenURL}
		if len(p.config.Scopes) == 0 {
			p.config.Scopes = []string{"read:user", "user:email"}
		}
		p.identity = githubIdentity
		return p, nil
	}
	issuer := cfg.Issuer
	if name == "google" {
		issuer = GoogleIssuer
	}
	if issuer == "" {
		return nil, fmt.Errorf("provider %q needs an issuer", name)
	}
	d, err := discover(ctx, client, issuer)
	if err != nil {
		return nil, err
	}
	p.config.Endpoint = oauth2.Endpoint{AuthURL: d.AuthorizationEndpoint, TokenURL: d.TokenEndpoint}
	if len(p.config.Scopes) == 0 {
		p.config.Scopes = []string{"openid", "email", "profile"}
	}
	p.userinfo = d.UserinfoEndpoint
	p.identity = oidcIdentity
	return p, nil
}

// AuthURL is where to send the browser: state and the PKCE verifier tie
// the callback to this sign-in.
func (p *Provider) AuthURL(state, verifier string) string {
	return p.config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}

// Identity redeems a callback's code and returns the provider's account.
func (p *Provider) Identity(ctx context.Context, code, verifier string) (Identity, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.client)
	tok, err := p.config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("exchange code: %w", err)
	}
	id, err := p.identity(ctx, p, tok)
	if err == nil && id.Subject == "" {
		err = errors.New("the provider returned no account id")
	}
	return id, err
}

// getJSON fetches url with the access token into out.
func (p *Provider) getJSON(ctx context.Context, tok *oauth2.Token, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(out)
}

func oidcIdentity(ctx context.Context, p *Provider, tok *oauth2.Token) (Identity, error) {
	var info struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"` // bool, or "true" at some providers
	}
	if err := p.getJSON(ctx, tok, p.userinfo, &info); err != nil {
		return Identity{}, fmt.Errorf("userinfo: %w", err)
	}
	verified := info.EmailVerified == true || info.EmailVerified == "true"
	return Identity{Subject: info.Sub, Email: info.Email, EmailVerified: verified}, nil
}

func githubIdentity(ctx context.Context, p *Provider, tok *oauth2.Token) (Identity, error) {
	var user struct {
		ID int64 `json:"id"`
	}
	if err := p.getJSON(ctx, tok, GitHubAPI+"/user", &user); err != nil {
		return Identity{}, fmt.Errorf("github user: %w", err)
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := p.getJSON(ctx, tok, GitHubAPI+"/user/emails", &emails); err != nil {
		return Identity{}, fmt.Errorf("github emails: %w", err)
	}
	id := Identity{Subject: strconv.FormatInt(user.ID, 10)}
	if user.ID == 0 {
		id.Subject = ""
	}
	for _, e := range emails {
		if e.Primary {
			id.Email, id.EmailVerified = e.Email, e.Verified
		}
	}
	return id, nil
}

type discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

var (
	discoveryMu    sync.Mutex
	discoveryCache = map[string]cachedDiscovery{}
)

type cachedDiscovery struct {
	d       discovery
	fetched time.Time
}

// discover reads an issuer's OpenID configuration (cached for an hour).
func discover(ctx context.Context, client *http.Client, issuer string) (discovery, error) {
	issuer = strings.TrimRight(issuer, "/")
	discoveryMu.Lock()
	c, ok := discoveryCache[issuer]
	discoveryMu.Unlock()
	if ok && time.Since(c.fetched) < time.Hour {
		return c.d, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return discovery{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return discovery{}, fmt.Errorf("discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return discovery{}, fmt.Errorf("discovery: %s answered %d", issuer, resp.StatusCode)
	}
	var d discovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&d); err != nil {
		return discovery{}, fmt.Errorf("discovery: %w", err)
	}
	// The document must be the issuer's own (OpenID Connect Discovery §4.3).
	if strings.TrimRight(d.Issuer, "/") != issuer || d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.UserinfoEndpoint == "" {
		return discovery{}, fmt.Errorf("discovery: %s is not a complete OpenID configuration for this issuer", issuer)
	}
	discoveryMu.Lock()
	discoveryCache[issuer] = cachedDiscovery{d: d, fetched: time.Now()}
	discoveryMu.Unlock()
	return d, nil
}
