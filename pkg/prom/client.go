// Package prom is a minimal client for the Prometheus HTTP API, covering
// exactly what the detector needs: instant queries, reduced to one number
// for signals and to labeled samples for evidence.
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

// Sample is one labeled sample from an instant query's result vector.
type Sample struct {
	Labels map[string]string
	Value  float64
}

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
	samples, err := c.instant(ctx, promql)
	if err != nil {
		return 0, err
	}
	if len(samples) > 1 {
		return 0, fmt.Errorf("prometheus: %d samples, catalog signals must aggregate to one", len(samples))
	}
	return samples[0].Value, nil
}

// QuerySeries runs an instant query and returns every sample with its
// labels intact. Evidence queries use it: a per-workload breakdown is only
// evidence if the workload names survive into the report.
func (c *Client) QuerySeries(ctx context.Context, promql string) ([]Sample, error) {
	return c.instant(ctx, promql)
}

// instant runs the query and decodes the full result vector. An empty
// vector is ErrNoData.
func (c *Client) instant(ctx context.Context, promql string) ([]Sample, error) {
	u := c.base + "/api/v1/query?query=" + url.QueryEscape(promql)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: %s", resp.Status)
	}

	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("prometheus: decoding response: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus: response status %q", body.Status)
	}
	if body.Data.ResultType != "vector" {
		return nil, fmt.Errorf("prometheus: unexpected result type %q", body.Data.ResultType)
	}
	if len(body.Data.Result) == 0 {
		return nil, ErrNoData
	}
	samples := make([]Sample, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		s, ok := r.Value[1].(string)
		if !ok {
			return nil, fmt.Errorf("prometheus: malformed sample value")
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("prometheus: malformed sample value: %w", err)
		}
		samples = append(samples, Sample{Labels: r.Metric, Value: v})
	}
	return samples, nil
}
