package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const upstreamTimeout = 2 * time.Second

// UpstreamResult mirrors one completed HTTP exchange with Djev (any status).
type UpstreamResult struct {
	Parsed   any
	RawBody  *string
	Status   *int
	ElapsedS float64
	Usage    map[string]any
	Error    *string
}

// Upstream sends the exact payload bytes; a returned error means no HTTP response was obtained.
type Upstream interface {
	Call(ctx context.Context, body []byte) (*UpstreamResult, error)
}

type httpUpstream struct {
	url    string
	apiKey string
	client *http.Client
}

// newHTTPUpstream reuses keep-alive connections: a fresh TCP connection per decision was measurable latency.
func newHTTPUpstream(baseURL, apiKey string) *httpUpstream {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	return &httpUpstream{
		url:    strings.TrimRight(baseURL, "/") + "/v1/systemone",
		apiKey: apiKey,
		client: &http.Client{Transport: transport, Timeout: upstreamTimeout},
	}
}

func (u *httpUpstream) Call(ctx context.Context, body []byte) (*UpstreamResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if u.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+u.apiKey)
	}
	started := time.Now()
	response, err := u.client.Do(request)
	if err != nil {
		return nil, transportError(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, transportError(err)
	}
	elapsed := time.Since(started).Seconds()
	text := strings.ToValidUTF8(string(raw), "�")
	status := response.StatusCode
	result := &UpstreamResult{RawBody: &text, Status: &status, ElapsedS: elapsed}
	parsed, parseErr := decodeJSON(raw)
	if parseErr != nil {
		message := fmt.Sprintf("JSONDecodeError: %v", parseErr)
		result.Error = &message
	} else {
		result.Parsed = parsed
	}
	if status < 200 || status >= 300 {
		if result.Error == nil {
			message := fmt.Sprintf("HTTPError: %d", status)
			result.Error = &message
		}
	}
	result.Usage = usageFromPayload(result.Parsed)
	return result, nil
}

func transportError(err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return fmt.Errorf("TimeoutError: %v", err)
	}
	return fmt.Errorf("URLError: %v", err)
}

func usageCount(value any) any {
	number, ok := numberValue(value)
	if !ok || number < 0 {
		return nil
	}
	return int64(number)
}

func usageFromPayload(payload any) map[string]any {
	usage := map[string]any{}
	if root, ok := payload.(map[string]any); ok {
		if object, ok := root["usage"].(map[string]any); ok {
			usage = object
		}
	}
	return map[string]any{"input_tokens": usageCount(usage["input_tokens"]), "output_tokens": usageCount(usage["output_tokens"])}
}

func tokenThroughput(usage map[string]any, elapsedS float64) any {
	input, inOK := usage["input_tokens"].(int64)
	output, outOK := usage["output_tokens"].(int64)
	if !inOK || !outOK || elapsedS <= 0 {
		return nil
	}
	return round(float64(input+output)/elapsedS, 1)
}
