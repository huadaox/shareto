package ipc

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

type Client struct {
	httpClient *http.Client
	sockPath   string
}

// NewClient creates an IPC client that dials the Unix socket.
func NewClient(customSockPath ...string) *Client {
	sockPath := DefaultSocketPath()
	if len(customSockPath) > 0 && customSockPath[0] != "" {
		sockPath = customSockPath[0]
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialVerified(ctx, sockPath, 2*time.Second)
		},
		DisableKeepAlives: true,
	}

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		},
		sockPath: sockPath,
	}
}

func (c *Client) Ping() bool {
	conn, err := dialVerified(context.Background(), c.sockPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func dialVerified(ctx context.Context, path string, timeout time.Duration) (net.Conn, error) {
	address, err := socketAddress(path)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", address)
	if err != nil {
		return nil, err
	}
	if err := verifyPeerUID(conn, currentUserID()); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (c *Client) Share(ctx context.Context, req ShareRequest) (*ShareResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "http://unix/ipc/share", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, parseErrorResponse(body, resp.StatusCode)
	}

	var res ShareResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) List(ctx context.Context) ([]ShareItemResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", "http://unix/ipc/list", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, parseErrorResponse(body, resp.StatusCode)
	}

	var res []ShareItemResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *Client) Revoke(ctx context.Context, tokenOrTarget string) (*RevokeResponse, error) {
	data, err := json.Marshal(RevokeRequest{Token: tokenOrTarget})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "http://unix/ipc/revoke", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, parseErrorResponse(body, resp.StatusCode)
	}

	var res RevokeResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", "http://unix/ipc/status", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, parseErrorResponse(body, resp.StatusCode)
	}

	var res StatusResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func parseErrorResponse(body []byte, statusCode int) error {
	var errMap map[string]string
	if err := json.Unmarshal(body, &errMap); err == nil {
		if msg, ok := errMap["error"]; ok {
			return errors.New(msg)
		}
	}
	return fmt.Errorf("request failed with status %d: %s", statusCode, string(body))
}
