package broker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
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

func (c *Client) Action(ctx context.Context, token, id string, parameters map[string]string, confirmation string) error {
	return c.do(ctx, http.MethodPost, "/v1/actions/"+id, token, ActionRequest{Parameters: parameters, Confirmation: confirmation}, nil)
}

func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/health", "", nil, nil)
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

func (c *Client) StreamQuery(ctx context.Context, token, id string, parameters map[string]string) (StreamResult, error) {
	encoded, err := json.Marshal(QueryRequest{Parameters: parameters})
	if err != nil {
		return StreamResult{}, fmt.Errorf("encode broker stream query: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/stream-queries/"+id, bytes.NewReader(encoded))
	if err != nil {
		return StreamResult{}, fmt.Errorf("create broker stream query: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return StreamResult{}, fmt.Errorf("%w at %s: %v", ErrUnavailable, c.socket, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer func() { _ = response.Body.Close() }()
		return StreamResult{}, streamResponseError(response)
	}
	if response.ContentLength < 0 {
		_ = response.Body.Close()
		return StreamResult{}, fmt.Errorf("decode broker stream response: missing content length")
	}
	filename, err := base64.RawURLEncoding.DecodeString(response.Header.Get(StreamNameHeader))
	if err != nil {
		_ = response.Body.Close()
		return StreamResult{}, fmt.Errorf("decode broker stream filename: %w", err)
	}
	return StreamResult{Body: newStreamBody(ctx, response.Body), Filename: string(filename), MediaType: response.Header.Get("Content-Type"), Size: response.ContentLength}, nil
}

func (c *Client) StreamAction(ctx context.Context, token, id string, parameters map[string]string, body io.Reader) error {
	encoded, err := json.Marshal(QueryRequest{Parameters: parameters})
	if err != nil {
		return fmt.Errorf("encode broker stream action: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/stream-actions/"+id, body)
	if err != nil {
		return fmt.Errorf("create broker stream action: %w", err)
	}
	if sized, ok := body.(interface{ Len() int }); ok {
		request.ContentLength = int64(sized.Len())
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(StreamMetadataHeader, base64.RawURLEncoding.EncodeToString(encoded))
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w at %s: %v", ErrUnavailable, c.socket, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return streamResponseError(response)
	}
	return nil
}

func streamResponseError(response *http.Response) error {
	limited := io.LimitReader(response.Body, 4<<10)
	var brokerError ErrorResponse
	_ = json.NewDecoder(limited).Decode(&brokerError)
	if brokerError.Error == "" {
		brokerError.Error = response.Status
	}
	err := NewPublicError(response.StatusCode, brokerError.Error, "", nil)
	if response.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: %w", ErrUnauthorized, err)
	}
	return err
}

type streamBody struct {
	body io.ReadCloser
	done chan struct{}
	once sync.Once
}

func newStreamBody(ctx context.Context, body io.ReadCloser) *streamBody {
	result := &streamBody{body: body, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = result.Close()
		case <-result.done:
		}
	}()
	return result
}

func (b *streamBody) Read(p []byte) (int, error) { return b.body.Read(p) }

func (b *streamBody) Close() error {
	var err error
	b.once.Do(func() {
		close(b.done)
		err = b.body.Close()
	})
	return err
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
		return streamResponseError(response)
	}
	if responseBody != nil {
		if err := json.NewDecoder(limited).Decode(responseBody); err != nil {
			return fmt.Errorf("decode broker response: %w", err)
		}
	}
	return nil
}
