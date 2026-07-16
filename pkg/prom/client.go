// Package prom is a minimal client for the Prometheus HTTP API, covering
// exactly what the detector needs: an instant query reduced to one number.
package prom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrNoData reports a query that returned an empty vector. Callers decide
// whether absence of data means healthy or broken; the client does not guess.
var ErrNoData = errors.New("query returned no samples")

// Client talks to one Prometheus server.
type Client struct {
	base string
	http *http.Client
}

// NewClient builds a client for the given base URL, e.g. http://localhost:9090.
func NewClient(baseURL string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// Query runs an instant query and returns the single sample's value.
// A result with more than one sample is an error: catalog signals must
// aggregate, otherwise the threshold comparison is meaningless.
func (c *Client) Query(ctx context.Context, promql string) (float64, error) {
	u := c.base + "/api/v1/query?query=" + url.QueryEscape(promql)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("prometheus: %s", resp.Status)
	}

	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("prometheus: decoding response: %w", err)
	}
	if body.Status != "success" {
		return 0, fmt.Errorf("prometheus: response status %q", body.Status)
	}
	if body.Data.ResultType != "vector" {
		return 0, fmt.Errorf("prometheus: unexpected result type %q", body.Data.ResultType)
	}
	results := body.Data.Result
	if len(results) == 0 {
		return 0, ErrNoData
	}
	if len(results) > 1 {
		return 0, fmt.Errorf("prometheus: %d samples, catalog signals must aggregate to one", len(results))
	}
	s, ok := results[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("prometheus: malformed sample value")
	}
	return strconv.ParseFloat(s, 64)
}
