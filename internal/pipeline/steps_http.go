package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

const maxHTTPResponseBody = 10 * 1024 * 1024 // 10 MB

var validHTTPMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

func ExecuteHTTPRequest(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	// 1. Validate and normalize method
	method := strings.ToUpper(strings.TrimSpace(step.HTTPMethod))
	if method == "" {
		return fmt.Errorf("http.request: method is required")
	}
	if !validHTTPMethods[method] {
		return fmt.Errorf("http.request: invalid method %q (must be GET, POST, PUT, PATCH, or DELETE)", method)
	}

	// 2. Interpolate URL
	rawURL, err := resolveStringValue(step.URL, pCtx)
	if err != nil {
		return fmt.Errorf("http.request: url interpolation failed: %w", err)
	}
	if rawURL == "" {
		return fmt.Errorf("http.request: url is required")
	}

	// Interpolate headers, query params, and body values
	interpolatedHeaders := make(map[string]string, len(step.Headers))
	for k, v := range step.Headers {
		resolved, err := resolveStringValue(v, pCtx)
		if err != nil {
			return fmt.Errorf("http.request: header %q interpolation failed: %w", k, err)
		}
		interpolatedHeaders[k] = resolved
	}

	interpolatedQuery := make(map[string]string, len(step.QueryParams))
	for k, v := range step.QueryParams {
		resolved, err := resolveStringValue(v, pCtx)
		if err != nil {
			return fmt.Errorf("http.request: query param %q interpolation failed: %w", k, err)
		}
		interpolatedQuery[k] = resolved
	}

	interpolatedBody := make(map[string]string, len(step.Body))
	for k, v := range step.Body {
		resolved, err := resolveStringValue(v, pCtx)
		if err != nil {
			return fmt.Errorf("http.request: body key %q interpolation failed: %w", k, err)
		}
		interpolatedBody[k] = resolved
	}

	// 3. Build request URL with query params
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("http.request: invalid url %q: %w", rawURL, err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("http.request: url must use http or https")
	}
	if len(interpolatedQuery) > 0 {
		q := parsedURL.Query()
		for k, v := range interpolatedQuery {
			q.Set(k, v)
		}
		parsedURL.RawQuery = q.Encode()
	}
	finalURL := parsedURL.String()

	// 4. Build request body (POST/PUT/PATCH/DELETE only)
	var bodyReader io.Reader
	hasRawBody := strings.TrimSpace(step.BodyRaw) != ""
	needsBody := method != "GET" && (len(interpolatedBody) > 0 || hasRawBody)
	if needsBody {
		contentType := findHeaderValue(interpolatedHeaders, "content-type")
		switch {
		case hasRawBody:
			// Raw templated body — sent verbatim after {{...}} interpolation.
			// Lets callers send nested JSON the StringMap body can't express.
			raw, err := resolveStringValue(step.BodyRaw, pCtx)
			if err != nil {
				return fmt.Errorf("http.request: body_raw interpolation failed: %w", err)
			}
			bodyReader = strings.NewReader(raw)
			if contentType == "" {
				interpolatedHeaders["Content-Type"] = "application/json"
			}
		case strings.Contains(strings.ToLower(contentType), "application/x-www-form-urlencoded"):
			form := url.Values{}
			for k, v := range interpolatedBody {
				form.Set(k, v)
			}
			bodyReader = strings.NewReader(form.Encode())
		default:
			// JSON body — convert map[string]string to map[string]any for marshaling
			jsonBody := make(map[string]any, len(interpolatedBody))
			for k, v := range interpolatedBody {
				jsonBody[k] = v
			}
			b, err := json.Marshal(jsonBody)
			if err != nil {
				return fmt.Errorf("http.request: failed to marshal body: %w", err)
			}
			bodyReader = bytes.NewReader(b)
			// Auto-set Content-Type if not provided by caller
			if contentType == "" {
				interpolatedHeaders["Content-Type"] = "application/json"
			}
		}
	}

	// 5. Create request with pipeline context (for timeout propagation)
	req, err := http.NewRequestWithContext(ctx, method, finalURL, bodyReader)
	if err != nil {
		return fmt.Errorf("http.request: failed to create request: %w", err)
	}

	// 6. Apply headers
	for k, v := range interpolatedHeaders {
		req.Header.Set(k, v)
	}

	// 7. Execute
	stepLabel := step.Assign
	if stepLabel == "" {
		stepLabel = "(no assign)"
	}
	client := httpClient
	if templatedHost(step.URL) {
		client = strictHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http.request step failed: %s: %w", stepLabel, err)
	}
	defer resp.Body.Close()

	// 8. Read response body with size limit
	limitedReader := io.LimitReader(resp.Body, maxHTTPResponseBody+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return fmt.Errorf("http.request step failed: %s: reading response: %w", stepLabel, err)
	}
	if int64(len(data)) > maxHTTPResponseBody {
		return fmt.Errorf("http.request: response body exceeds 10MB limit")
	}

	// 9. Parse response
	if step.Assign == "" {
		return nil
	}

	var result any
	if len(data) > 0 {
		respContentType := resp.Header.Get("Content-Type")
		if strings.Contains(respContentType, "application/json") {
			var parsed any
			if err := json.Unmarshal(data, &parsed); err == nil {
				result = parsed
			} else {
				result = string(data)
			}
		} else {
			result = string(data)
		}
	}

	// 10. Assign to context with __status
	statusCode := resp.StatusCode
	var stored map[string]any

	switch v := result.(type) {
	case map[string]any:
		// JSON object — merge __status directly
		v["__status"] = statusCode
		stored = v
	default:
		// Array, scalar, string, or nil — wrap in a container
		stored = map[string]any{"__status": statusCode}
		if result != nil {
			stored["__body"] = result
		}
	}

	pCtx.Set(step.Assign, stored)
	return nil
}

// resolveStringValue resolves a potentially-templated string to a plain string.
// Uses ResolveString and coerces the result to string.
func resolveStringValue(s string, pCtx *PipelineContext) (string, error) {
	if s == "" {
		return "", nil
	}
	resolved, err := ResolveString(s, pCtx)
	if err != nil {
		return "", err
	}
	if resolved == nil {
		return "", nil
	}
	switch v := resolved.(type) {
	case string:
		return v, nil
	default:
		return fmt.Sprintf("%v", v), nil
	}
}

// findHeaderValue looks up a header value case-insensitively.
func findHeaderValue(headers map[string]string, lowerKey string) string {
	for k, v := range headers {
		if strings.ToLower(k) == lowerKey {
			return v
		}
	}
	return ""
}
