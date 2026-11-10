// Package http is the server repository over HTTP: the agent's client for
// vending-server's machine API, authenticated with the machine key.
package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vending-machine/internal/model"
	"vending-machine/internal/repository/server"
)

// Client calls one server with one machine key. Build one in cmd/main.
type Client struct {
	base      string
	key       string
	userAgent string
	hc        *http.Client
}

var _ server.Repository = (*Client)(nil)

const (
	maxBody  = 8 << 20
	maxImage = 6 << 20
)

// New returns a client for baseURL (e.g. https://35.219.98.32). insecureTLS
// skips certificate checks, for a server with a self-signed certificate only.
func New(baseURL, machineKey string, insecureTLS bool, version string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("serverUrl %q: use http://host:port or https://host", baseURL)
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	if insecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for a self-signed demo server
	}
	return &Client{
		base:      u.String(),
		key:       machineKey,
		userAgent: "vending-machine/" + version,
		hc:        &http.Client{Transport: transport},
	}, nil
}

// BaseURL is the server address.
func (c *Client) BaseURL() string { return c.base }

// ---------------------------------------------------------------- typed calls

func (c *Client) Heartbeat(ctx context.Context) (int64, error) {
	var hb struct {
		CatalogVersion int64 `json:"catalogVersion"`
	}
	err := c.json(ctx, http.MethodPost, "/api/machine/heartbeat", struct{}{}, nil, &hb)
	return hb.CatalogVersion, err
}

func (c *Client) Catalog(ctx context.Context) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/machine/catalog", nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, asAPIError(resp)
	}
	return resp.Body, nil
}

func (c *Client) Transaction(ctx context.Context, txID string) (model.TxState, error) {
	var st model.TxState
	err := c.json(ctx, http.MethodGet, server.TransactionPath(txID, ""), nil, nil, &st)
	return st, err
}

func (c *Client) Pay(ctx context.Context, txID, requestID string) (model.TxState, error) {
	var st model.TxState
	err := c.json(ctx, http.MethodPost, server.TransactionPath(txID, "pay"), struct{}{}, http.Header{"X-Request-Id": {requestID}}, &st)
	return st, err
}

func (c *Client) Cancel(ctx context.Context, txID, reason string) error {
	return c.json(ctx, http.MethodPost, server.TransactionPath(txID, "cancel"), map[string]string{"reason": reason}, nil, nil)
}

func (c *Client) Result(ctx context.Context, txID, status, reason string) error {
	body := map[string]string{"status": status}
	if reason != "" {
		body["failureReason"] = reason
	}
	return c.json(ctx, http.MethodPost, server.TransactionPath(txID, "result"), body, nil, nil)
}

// Image is a plain GET, without the machine key.
func (c *Client) Image(ctx context.Context, rel string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/uploads/"+rel, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, model.Offline(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &model.APIError{Status: resp.StatusCode, Code: fmt.Sprintf("HTTP_%d", resp.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImage))
	if err != nil {
		return nil, fmt.Errorf("read image %s: %w", rel, err)
	}
	return data, nil
}

// Forward sends the page's body and operator token as they came.
func (c *Client) Forward(ctx context.Context, method, path string, in model.ForwardRequest) (*model.Response, error) {
	header := http.Header{"X-Request-Id": {in.RequestID}}
	if in.Authorization != "" {
		header.Set("Authorization", in.Authorization)
	}
	body := in.Body
	if method == http.MethodGet {
		body = nil
	}
	return c.do(ctx, method, path, body, header)
}

// ---------------------------------------------------------------- transport

// do sends one request and reads the whole answer. Every request carries the
// machine key; header adds Authorization (an operator token) or X-Request-ID.
func (c *Client) do(ctx context.Context, method, path string, body []byte, header http.Header) (*model.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("X-Machine-Key", c.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, model.Offline(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, model.Offline(err)
	}
	// Caddy answers 502 when the Go server is stopped; that is "offline" too.
	// The server's own errors always carry a JSON code.
	if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout {
		if code, _ := errorCode(data); code == "" {
			return nil, model.Offline(fmt.Errorf("HTTP %d from %s", resp.StatusCode, c.base))
		}
	}
	return &model.Response{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: data}, nil
}

// json sends in (nil for no body) and decodes a 2xx answer into out (may be
// nil). A non-2xx answer is a *model.APIError.
func (c *Client) json(ctx context.Context, method, path string, in any, header http.Header, out any) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = b
	}
	resp, err := c.do(ctx, method, path, body, header)
	if err != nil {
		return err
	}
	if resp.Status < 200 || resp.Status > 299 {
		return asAPIError(resp)
	}
	if out != nil && len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

func asAPIError(resp *model.Response) *model.APIError {
	code, msg := errorCode(resp.Body)
	if code == "" {
		code = fmt.Sprintf("HTTP_%d", resp.Status)
	}
	return &model.APIError{Status: resp.Status, Code: code, Message: msg, Body: resp.Body}
}

func errorCode(body []byte) (code, message string) {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) != nil {
		return "", ""
	}
	return e.Code, e.Message
}
