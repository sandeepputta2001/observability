package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// FunctionSample represents a profiling sample for a single function.
type FunctionSample struct {
	FunctionName string
	File         string
	SelfNs       int64
	TotalNs      int64
	SelfPercent  float64
}

// pyroscopeRenderResponse is the Pyroscope render API response.
type pyroscopeRenderResponse struct {
	Flamebearer struct {
		Names   []string `json:"names"`
		Levels  [][]int  `json:"levels"`
		NumTics int      `json:"numTics"`
		MaxSelf int      `json:"maxSelf"`
	} `json:"flamebearer"`
	Metadata struct {
		Format     string `json:"format"`
		SampleRate int    `json:"sampleRate"`
		Units      string `json:"units"`
	} `json:"metadata"`
}

// PyroscopeClient is an HTTP client for Pyroscope profile queries.
type PyroscopeClient struct {
	baseURL string
	client  *http.Client
}

// NewPyroscopeClient creates a PyroscopeClient pointing at the given base URL.
func NewPyroscopeClient(baseURL string) *PyroscopeClient {
	return &PyroscopeClient{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// GetProfile fetches a pprof-format profile for the given app and profile type.
// Returns the raw pprof bytes which can be parsed with google/pprof.
func (c *PyroscopeClient) GetProfile(ctx context.Context, app, profileType string, from, until time.Time) ([]byte, error) {
	params := url.Values{}
	params.Set("name", app+"."+profileType)
	params.Set("from", strconv.FormatInt(from.Unix(), 10))
	params.Set("until", strconv.FormatInt(until.Unix(), 10))
	params.Set("format", "pprof")

	u := c.baseURL + "/pyroscope/render?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating pyroscope request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing pyroscope request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pyroscope returned status %d: %s", resp.StatusCode, body)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading pyroscope response: %w", err)
	}
	return data, nil
}

// GetTopFunctions returns the top CPU-consuming functions for an app in the given time range.
func (c *PyroscopeClient) GetTopFunctions(ctx context.Context, app string, from, until time.Time, limit int) ([]FunctionSample, error) {
	params := url.Values{}
	params.Set("name", app+".cpu")
	params.Set("from", strconv.FormatInt(from.Unix(), 10))
	params.Set("until", strconv.FormatInt(until.Unix(), 10))
	params.Set("format", "json")

	u := c.baseURL + "/pyroscope/render?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating pyroscope top functions request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing pyroscope top functions request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading pyroscope response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pyroscope returned status %d: %s", resp.StatusCode, body)
	}

	var renderResp pyroscopeRenderResponse
	if err := json.Unmarshal(body, &renderResp); err != nil {
		return nil, fmt.Errorf("decoding pyroscope response: %w", err)
	}

	samples := extractTopFunctions(renderResp, limit)
	return samples, nil
}

// extractTopFunctions parses the Pyroscope flamebearer format into FunctionSample list.
func extractTopFunctions(resp pyroscopeRenderResponse, limit int) []FunctionSample {
	names := resp.Flamebearer.Names
	if len(names) == 0 {
		return nil
	}

	selfMap := make(map[string]int64, len(names))
	totalMap := make(map[string]int64, len(names))

	// Flamebearer levels: each level is [x, total, self, name_idx, ...]
	for _, level := range resp.Flamebearer.Levels {
		for i := 0; i+3 < len(level); i += 4 {
			nameIdx := level[i+3]
			if nameIdx < 0 || nameIdx >= len(names) {
				continue
			}
			name := names[nameIdx]
			selfMap[name] += int64(level[i+2])
			totalMap[name] += int64(level[i+1])
		}
	}

	totalSamples := int64(resp.Flamebearer.NumTics)
	if totalSamples == 0 {
		totalSamples = 1
	}

	var samples []FunctionSample
	for name, self := range selfMap {
		samples = append(samples, FunctionSample{
			FunctionName: name,
			SelfNs:       self,
			TotalNs:      totalMap[name],
			SelfPercent:  float64(self) / float64(totalSamples) * 100,
		})
	}

	sort.Slice(samples, func(i, j int) bool {
		return samples[i].SelfNs > samples[j].SelfNs
	})

	if limit > 0 && len(samples) > limit {
		samples = samples[:limit]
	}
	return samples
}
