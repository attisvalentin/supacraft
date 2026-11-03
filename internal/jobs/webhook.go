package jobs

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

	"github.com/attisvalentin/supacraft/internal/pipeline"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

const deliveryTimeout = 15 * time.Second

// Signature is the X-SupaCraft-Signature value for body sent at ts:
// "t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>.<body>" with secret>".
// Receivers recompute it and reject old timestamps to stop replays.
func Signature(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

// deliver POSTs a webhook job's event. Any 2xx is success; anything else
// (including redirects, which are not followed) fails the attempt.
func deliver(ctx context.Context, cfg *tenant.TenantConfig, name string, j tenant.Job) error {
	w, ok := cfg.APISettings.Webhooks[name]
	if !ok {
		return fmt.Errorf("webhook %q no longer exists", name)
	}
	u, err := url.Parse(w.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("webhook %q has no valid http(s) url", name)
	}
	body, err := json.Marshal(j.Args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	event, _ := j.Args["event"].(string)
	delivery, _ := j.Args["id"].(string)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SupaCraft-Webhook/1")
	req.Header.Set("X-SupaCraft-Event", event)
	req.Header.Set("X-SupaCraft-Delivery", delivery)
	req.Header.Set("X-SupaCraft-Attempt", strconv.Itoa(j.Attempts))
	if w.Secret != "" {
		req.Header.Set("X-SupaCraft-Signature", Signature(w.Secret, time.Now().Unix(), body))
	}
	client := *pipeline.ClientFor(ctx, w.URL)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook %s: %w", name, err)
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook %s: %s responded %d %s", name, u.Host, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
