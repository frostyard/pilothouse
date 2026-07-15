package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

var ErrUnauthorized = errors.New("authentication required")
var ErrUnavailable = errors.New("privileged broker unavailable")

type Client struct {
	baseURL string
	http    *http.Client
	socket  string
}

func NewClient(socket string) *Client {
	transport := &http.Transport{
		DisableCompression: true,
		DisableKeepAlives:  false,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
		},
	}
	return &Client{baseURL: "http://unix", http: &http.Client{Transport: transport}, socket: socket}
}

func (c *Client) Action(ctx context.Context, token, id string, parameters map[string]string) error {
	return c.do(ctx, http.MethodPost, "/v1/actions/"+id, token, ActionRequest{Parameters: parameters}, nil)
}

func (c *Client) Login(ctx context.Context, username, password, remote string) (LoginResponse, error) {
	var response LoginResponse
	err := c.do(ctx, http.MethodPost, "/v1/login", "", LoginRequest{Password: password, Remote: remote, Username: username}, &response)
	return response, err
}

func (c *Client) Logout(ctx context.Context, token string) error {
	return c.do(ctx, http.MethodPost, "/v1/logout", token, struct{}{}, nil)
}

func (c *Client) Query(ctx context.Context, token, id string, parameters map[string]string, target any) error {
	var response QueryResponse
	if err := c.do(ctx, http.MethodPost, "/v1/queries/"+id, token, QueryRequest{Parameters: parameters}, &response); err != nil {
		return err
	}
	if err := json.Unmarshal(response.Result, target); err != nil {
		return fmt.Errorf("decode broker query result: %w", err)
	}
	return nil
}

func (c *Client) Session(ctx context.Context, token string) (SessionResponse, error) {
	var response SessionResponse
	err := c.do(ctx, http.MethodGet, "/v1/session", token, nil, &response)
	return response, err
}

func (c *Client) do(ctx context.Context, method, path, token string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode broker request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create broker request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w at %s: %v", ErrUnavailable, c.socket, err)
	}
	defer func() { _ = response.Body.Close() }()
	limited := io.LimitReader(response.Body, 2<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var brokerError ErrorResponse
		_ = json.NewDecoder(limited).Decode(&brokerError)
		if response.StatusCode == http.StatusUnauthorized {
			return ErrUnauthorized
		}
		if brokerError.Error == "" {
			brokerError.Error = response.Status
		}
		return fmt.Errorf("broker: %s", brokerError.Error)
	}
	if responseBody != nil {
		if err := json.NewDecoder(limited).Decode(responseBody); err != nil {
			return fmt.Errorf("decode broker response: %w", err)
		}
	}
	return nil
}
