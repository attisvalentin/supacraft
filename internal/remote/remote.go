// Package remote calls the signed /admin endpoints of a central SupaCraft
// server (see internal/studiosign for the scheme).
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/attisvalentin/supacraft/internal/studiosign"
)

// Client talks to one server with SUPACRAFT_ADMIN_SECRET.
type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

// New checks the server URL and returns a client. Plain http is only allowed
// for localhost, since requests carry secret values.
func New(server, secret string) (*Client, error) {
	if server == "" {
		return nil, fmt.Errorf("no server: pass --server or set SUPACRAFT_SERVER")
	}
	if secret == "" {
		return nil, fmt.Errorf("SUPACRAFT_ADMIN_SECRET is not set (use the same value as on the server)")
	}
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid server URL %q", server)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if host := u.Hostname(); host != "localhost" && !isLoopback(host) {
			return nil, fmt.Errorf("refusing plain http to %s: secrets would travel unencrypted (use https)", host)
		}
	default:
		return nil, fmt.Errorf("server URL must start with https://")
	}
	return &Client{baseURL: u.String(), secret: secret, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Error is a non-2xx response.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Do sends a signed request; reqBody and out are JSON (either may be nil).
func (c *Client) Do(ctx context.Context, method, path string, reqBody, out any) error {
	var body []byte
	if reqBody != nil {
		var err error
		if body, err = json.Marshal(reqBody); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ts := time.Now().Unix()
	nonce := studiosign.NewNonce()
	req.Header.Set(studiosign.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(studiosign.HeaderNonce, nonce)
	req.Header.Set(studiosign.HeaderSignature, studiosign.Sign(c.secret, ts, nonce, method, req.URL.RequestURI(), body))
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		msg := resp.Status
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &Error{Status: resp.StatusCode, Message: fmt.Sprintf("server: %s", msg)}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
