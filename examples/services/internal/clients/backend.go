package clients

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Count contacts an operator-configured endpoint, never a URL from an HTTP
// request/task payload. The example endpoint returns public demo data only.
func Count(ctx context.Context, base string) (int64, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return 0, errors.New("invalid backend URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/products/count/"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return 0, errors.New("invalid backend request")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return 0, errors.New("backend request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, errors.New("backend request rejected")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return 0, errors.New("invalid backend response")
	}
	var result struct {
		Count *int64 `json:"count"`
	}
	if json.Unmarshal(body, &result) != nil || result.Count == nil || *result.Count < 0 {
		return 0, errors.New("invalid backend response")
	}
	return *result.Count, nil
}
