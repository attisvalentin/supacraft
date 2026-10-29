package studio

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ProxySource implements DataSource by calling the /admin/studio endpoints on a
// remote SupaCraft API over HTTPS, instead of reading the database directly. This
// is for deployments where the DB isn't reachable from the operator's machine.
// Requests are HMAC-authenticated:
// X-Signature = "sha256=" + hex(HMAC-SHA256(secret, "<unix-ts>.<body>")).
type ProxySource struct {
	baseURL string
	secret  string
	client  *http.Client
}

// NewProxySource targets the API at baseURL (e.g. https://api.example.com),
// signing each request with secret (the server's STUDIO_SECRET).
func NewProxySource(baseURL, secret string) (*ProxySource, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("proxy base URL required")
	}
	if secret == "" {
		return nil, fmt.Errorf("studio secret required for proxy mode (set STUDIO_SECRET)")
	}
	return &ProxySource{
		baseURL: strings.TrimRight(baseURL, "/"),
		secret:  secret,
		client:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

type proxyFilter struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

type proxyQueryReq struct {
	Table   string        `json:"table"`
	Limit   int           `json:"limit"`
	Offset  int           `json:"offset"`
	OrderBy string        `json:"order_by"`
	Desc    bool          `json:"desc"`
	Filters []proxyFilter `json:"filters"`
}

func (s *ProxySource) ListTenants(ctx context.Context) ([]TenantSummary, error) {
	out := []TenantSummary{}
	if err := s.do(ctx, http.MethodGet, "/admin/studio/tenants", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ProxySource) GetSchema(ctx context.Context, tenantID string) (TenantSchema, error) {
	var out TenantSchema
	path := "/admin/studio/tenants/" + url.PathEscape(tenantID) + "/schema"
	err := s.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (s *ProxySource) QueryRows(ctx context.Context, tenantID, table string, opts QueryOptions) (RowsPage, error) {
	body := proxyQueryReq{
		Table:   table,
		Limit:   opts.Limit,
		Offset:  opts.Offset,
		OrderBy: opts.OrderBy,
		Desc:    opts.Desc,
	}
	for _, f := range opts.Filters {
		body.Filters = append(body.Filters, proxyFilter{Column: f.Column, Op: f.Op, Value: f.Value})
	}
	var out RowsPage
	path := "/admin/studio/tenants/" + url.PathEscape(tenantID) + "/query"
	err := s.do(ctx, http.MethodPost, path, body, &out)
	return out, err
}

func (s *ProxySource) Close() error { return nil }

func (s *ProxySource) do(ctx context.Context, method, path string, reqBody, out any) error {
	var bodyBytes []byte
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyBytes = b
	}

	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("X-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Signature", proxySign(s.secret, ts, bodyBytes))
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("proxy request failed: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		msg := ""
		if json.Unmarshal(data, &e) == nil {
			msg = e.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("proxy %s %s returned %s", method, path, resp.Status)
		}
		// Preserve the upstream status so the local HTTP layer reports the same
		// code instead of inferring it from the message text.
		return &statusError{status: resp.StatusCode, msg: msg}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// statusError carries an upstream HTTP status code alongside the message so the
// local HTTP layer (statusForErr) can report the same code in proxy mode.
type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string { return e.msg }

func proxySign(secret string, ts int64, body []byte) string {
	payload := strconv.FormatInt(ts, 10) + "." + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
