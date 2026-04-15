package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// LogStream is a stream of log entries from Loki sharing the same label set.
type LogStream struct {
	Labels  map[string]string
	Entries []*LogEntry
}

// LogEntry is a single log line from Loki.
type LogEntry struct {
	Timestamp time.Time
	Line      string
}

// lokiResponse is the Loki HTTP query API response envelope.
type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"` // [[ts_ns_str, line], ...]
		} `json:"result"`
	} `json:"data"`
}

// LokiClient is an HTTP client for Loki log queries and tailing.
type LokiClient struct {
	baseURL string
	client  *http.Client
}

// NewLokiClient creates a LokiClient pointing at the given base URL.
func NewLokiClient(baseURL string) *LokiClient {
	return &LokiClient{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Query executes a LogQL instant query returning up to limit log lines.
func (c *LokiClient) Query(ctx context.Context, logql string, limit int) ([]*LogStream, error) {
	params := url.Values{}
	params.Set("query", logql)
	params.Set("limit", strconv.Itoa(limit))

	return c.queryAPI(ctx, "/loki/api/v1/query", params)
}

// QueryRange executes a LogQL range query between start and end.
func (c *LokiClient) QueryRange(ctx context.Context, logql string, start, end time.Time) ([]*LogStream, error) {
	params := url.Values{}
	params.Set("query", logql)
	params.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	params.Set("limit", "100")

	return c.queryAPI(ctx, "/loki/api/v1/query_range", params)
}

// TailLogs opens a WebSocket connection to Loki's tail endpoint and streams log lines.
// The returned channel is closed when ctx is cancelled or the connection drops.
func (c *LokiClient) TailLogs(ctx context.Context, logql string) (<-chan *LogEntry, error) {
	wsURL := "ws" + c.baseURL[4:] + "/loki/api/v1/tail" // http -> ws
	params := url.Values{}
	params.Set("query", logql)

	conn, _, err := websocket.Dial(ctx, wsURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("dialing loki tail websocket: %w", err)
	}

	ch := make(chan *LogEntry, 256)
	go func() {
		defer close(ch)
		defer conn.CloseNow()

		for {
			var msg struct {
				Streams []struct {
					Values [][]string `json:"values"`
				} `json:"streams"`
			}
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			for _, stream := range msg.Streams {
				for _, v := range stream.Values {
					if len(v) < 2 {
						continue
					}
					tsNs, err := strconv.ParseInt(v[0], 10, 64)
					if err != nil {
						continue
					}
					entry := &LogEntry{
						Timestamp: time.Unix(0, tsNs),
						Line:      v[1],
					}
					select {
					case ch <- entry:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return ch, nil
}

// queryAPI performs a Loki HTTP query and returns parsed log streams.
func (c *LokiClient) queryAPI(ctx context.Context, path string, params url.Values) ([]*LogStream, error) {
	u := c.baseURL + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating loki request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing loki request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading loki response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki returned status %d: %s", resp.StatusCode, body)
	}

	var lr lokiResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("decoding loki response: %w", err)
	}

	var streams []*LogStream
	for _, r := range lr.Data.Result {
		stream := &LogStream{Labels: r.Stream}
		for _, v := range r.Values {
			if len(v) < 2 {
				continue
			}
			tsNs, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			stream.Entries = append(stream.Entries, &LogEntry{
				Timestamp: time.Unix(0, tsNs),
				Line:      v[1],
			})
		}
		streams = append(streams, stream)
	}
	return streams, nil
}
