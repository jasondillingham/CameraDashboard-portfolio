package go2rtc

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

// Client provides access to the go2rtc API
type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

// ProducerInfo represents a producer in a go2rtc stream
type ProducerInfo struct {
	URL string `json:"url"`
}

// StreamInfo represents a stream in go2rtc
type StreamInfo struct {
	Name      string         `json:"name"`
	Sources   []string       `json:"sources,omitempty"`
	Producers []ProducerInfo `json:"producers,omitempty"`
}

// StreamsResponse is the response from /api/streams
type StreamsResponse map[string]StreamInfo

// NewClient creates a new go2rtc API client.
// The baseURL may include basic auth credentials (e.g. http://user:pass@host:port).
func NewClient(baseURL string) *Client {
	c := &Client{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}

	// Extract and strip basic auth credentials from URL if present
	if parsed, err := url.Parse(baseURL); err == nil && parsed.User != nil {
		c.username = parsed.User.Username()
		c.password, _ = parsed.User.Password()
		parsed.User = nil
		c.baseURL = parsed.String()
	} else {
		c.baseURL = baseURL
	}

	return c
}

// doRequest executes an HTTP request and checks the response status.
// Returns the response body; caller must close it.
func (c *Client) doRequest(method, endpoint string, okStatuses ...int) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	// Default acceptable status is 200
	if len(okStatuses) == 0 {
		okStatuses = []int{http.StatusOK}
	}

	for _, s := range okStatuses {
		if resp.StatusCode == s {
			return resp, nil
		}
	}

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
}

// HealthCheck verifies go2rtc is running and accessible
func (c *Client) HealthCheck() error {
	resp, err := c.doRequest(http.MethodGet, "/api/streams")
	if err != nil {
		return fmt.Errorf("go2rtc not reachable: %w", err)
	}
	resp.Body.Close()
	return nil
}

// GetStreams returns all configured streams
func (c *Client) GetStreams() (StreamsResponse, error) {
	resp, err := c.doRequest(http.MethodGet, "/api/streams")
	if err != nil {
		return nil, fmt.Errorf("failed to get streams: %w", err)
	}
	defer resp.Body.Close()

	var streams StreamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&streams); err != nil {
		return nil, fmt.Errorf("failed to decode streams response: %w", err)
	}

	return streams, nil
}

// AddStream adds or updates a stream in go2rtc
func (c *Client) AddStream(name, rtspURL string) error {
	return c.AddStreamWithSources(name, []string{rtspURL})
}

// AddStreamWithSources adds a stream with multiple sources in go2rtc
func (c *Client) AddStreamWithSources(name string, sources []string) error {
	endpoint := "/api/streams?name=" + url.QueryEscape(name)
	for _, src := range sources {
		endpoint += "&src=" + url.QueryEscape(src)
	}

	resp, err := c.doRequest(http.MethodPut, endpoint, http.StatusOK, http.StatusCreated)
	if err != nil {
		return fmt.Errorf("failed to add stream: %w", err)
	}
	resp.Body.Close()
	return nil
}

// DeleteStream removes a stream from go2rtc
func (c *Client) DeleteStream(name string) error {
	endpoint := "/api/streams?src=" + url.QueryEscape(name)

	resp, err := c.doRequest(http.MethodDelete, endpoint, http.StatusOK, http.StatusNoContent)
	if err != nil {
		return fmt.Errorf("failed to delete stream: %w", err)
	}
	resp.Body.Close()
	return nil
}

// StreamExists checks if a stream is already configured
func (c *Client) StreamExists(name string) (bool, error) {
	streams, err := c.GetStreams()
	if err != nil {
		return false, err
	}

	_, exists := streams[name]
	return exists, nil
}

// GetWebRTCURL returns the WebRTC API URL for a stream
func (c *Client) GetWebRTCURL(streamName string) string {
	return fmt.Sprintf("%s/api/webrtc?src=%s", c.baseURL, url.QueryEscape(streamName))
}

// GetWebRTCScriptURL returns the URL for the WebRTC client script
func (c *Client) GetWebRTCScriptURL() string {
	return c.baseURL + "/webrtc/webrtc.min.js"
}

// GetFrameJPEG captures a single JPEG frame from a stream
func (c *Client) GetFrameJPEG(streamName string) ([]byte, error) {
	endpoint := "/api/frame.jpeg?src=" + url.QueryEscape(streamName)

	resp, err := c.doRequest(http.MethodGet, endpoint)
	if err != nil {
		return nil, fmt.Errorf("frame capture failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read frame data: %w", err)
	}

	return data, nil
}

// Restart sends exit code 100 to go2rtc, which triggers systemd to restart it.
// All active WebSocket/RTSP connections are dropped.
func (c *Client) Restart() error {
	// go2rtc exits with code 100 which systemd interprets as restart.
	// The HTTP call typically fails with connection reset/refused because
	// go2rtc exits before sending a response — that's expected and logged.
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/exit?code=100", nil)
	if err != nil {
		return fmt.Errorf("failed to create restart request: %w", err)
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("go2rtc restart: connection dropped (expected): %v", err)
		return nil
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("go2rtc restart returned unexpected status %d", resp.StatusCode)
	}
	return nil
}

// BaseURL returns the go2rtc base URL
func (c *Client) BaseURL() string {
	return c.baseURL
}
