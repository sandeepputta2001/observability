package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// QueryResult holds the result of an instant MetricsQL query.
type QueryResult struct {
	Metric map[string]string
	Value  float64
	Time   time.Time
}

// RangeResult holds the result of a range MetricsQL query.
type RangeResult struct {
	Metric map[string]string
	Values []RangePoint
}

// RangePoint is a single (timestamp, value) pair in a range result.
type RangePoint struct {
	Time  time.Time
	Value float64
}

// vmResponse is the Prometheus-compatible API response envelope.
type vmResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`  // instant: [ts, "val"]
			Values [][]interface{}   `json:"values"` // range: [[ts, "val"], ...]
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

// VictoriaMetricsClient is an HTTP client for MetricsQL instant and range queries.
type VictoriaMetricsClient struct {
	baseURL string
	client  *http.Client
}

// NewVictoriaMetricsClient creates a client pointing at the given base URL.
func NewVictoriaMetricsClient(baseURL string) *VictoriaMetricsClient {
	return &VictoriaMetricsClient{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Query executes a MetricsQL instant query at time t.
func (c *VictoriaMetricsClient) Query(ctx context.Context, expr string, t time.Time) ([]*QueryResult, error) {
	params := url.Values{}
	params.Set("query", expr)
	params.Set("time", strconv.FormatInt(t.Unix(), 10))

	resp, err := c.get(ctx, "/api/v1/query", params)
	if err != nil {
		return nil, fmt.Errorf("instant query: %w", err)
	}

	var results []*QueryResult
	for _, r := range resp.Data.Result {
		if len(r.Value) < 2 {
			continue
		}
		val, err := strconv.ParseFloat(r.Value[1].(string), 64)
		if err != nil || math.IsNaN(val) {
			continue
		}
		ts := time.Unix(int64(r.Value[0].(float64)), 0)
		results = append(results, &QueryResult{
			Metric: r.Metric,
			Value:  val,
			Time:   ts,
		})
	}
	return results, nil
}

// QueryInstant executes a MetricsQL instant query and returns the first scalar result.
// Implements the VMQuerier interface used by SLOTracker and RuleEvaluator.
func (c *VictoriaMetricsClient) QueryInstant(ctx context.Context, expr string, t time.Time) (float64, error) {
	results, err := c.Query(ctx, expr, t)
	if err != nil {
		return 0, err
	}
	if len(results) == 0 {
		return 0, nil
	}
	return results[0].Value, nil
}

// QueryRange executes a MetricsQL range query.
func (c *VictoriaMetricsClient) QueryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]*RangeResult, error) {
	params := url.Values{}
	params.Set("query", expr)
	params.Set("start", strconv.FormatInt(start.Unix(), 10))
	params.Set("end", strconv.FormatInt(end.Unix(), 10))
	params.Set("step", strconv.FormatInt(int64(step.Seconds()), 10))

	resp, err := c.get(ctx, "/api/v1/query_range", params)
	if err != nil {
		return nil, fmt.Errorf("range query: %w", err)
	}

	var results []*RangeResult
	for _, r := range resp.Data.Result {
		rr := &RangeResult{Metric: r.Metric}
		for _, v := range r.Values {
			if len(v) < 2 {
				continue
			}
			val, err := strconv.ParseFloat(v[1].(string), 64)
			if err != nil || math.IsNaN(val) {
				continue
			}
			ts := time.Unix(int64(v[0].(float64)), 0)
			rr.Values = append(rr.Values, RangePoint{Time: ts, Value: val})
		}
		results = append(results, rr)
	}
	return results, nil
}

// LabelValues returns all values for a given label name (used for service discovery).
func (c *VictoriaMetricsClient) LabelValues(ctx context.Context, label string) ([]string, error) {
	params := url.Values{}
	resp, err := c.get(ctx, "/api/v1/label/"+label+"/values", params)
	if err != nil {
		return nil, fmt.Errorf("label values for %q: %w", label, err)
	}

	// Label values come back in Data.Result as strings via a different shape;
	// VictoriaMetrics returns them in the standard Prometheus label values format.
	var values []string
	raw, err := json.Marshal(resp.Data.Result)
	if err != nil {
		return nil, fmt.Errorf("marshalling label values: %w", err)
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("unmarshalling label values: %w", err)
	}
	return values, nil
}

// get performs a GET request to the VictoriaMetrics API and decodes the response.
func (c *VictoriaMetricsClient) get(ctx context.Context, path string, params url.Values) (*vmResponse, error) {
	u := c.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request to %q: %w", u, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("VictoriaMetrics returned status %d: %s", resp.StatusCode, body)
	}

	var vmResp vmResponse
	if err := json.Unmarshal(body, &vmResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if vmResp.Status != "success" {
		return nil, fmt.Errorf("VictoriaMetrics error: %s", vmResp.Error)
	}
	return &vmResp, nil
}
