package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

const (
	Email    = "admin@example.com"
	Password = "correct horse"
)

var tokens, transports sync.Map

func transport(addr string) *http.Transport {
	t, _ := transports.LoadOrStore(addr, http.DefaultTransport.(*http.Transport).Clone())

	return t.(*http.Transport)
}

func CloseIdleConnections(addr string) {
	if t, ok := transports.Load(addr); ok {
		t.(*http.Transport).CloseIdleConnections()
	}
}

func Client(addr string) *http.Client {
	return &http.Client{Transport: bearer{addr: addr}}
}

type bearer struct {
	addr string
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	token, ok := tokens.Load(b.addr)
	if !ok {
		return b.retry(r)
	}

	res, err := b.send(r, token.(string))
	if err != nil || res.StatusCode != http.StatusUnauthorized || r.Body != nil && r.GetBody == nil {
		return res, err
	}

	_ = res.Body.Close()

	return b.retry(r)
}

func (b bearer) retry(r *http.Request) (*http.Response, error) {
	token, err := logIn(r.Context(), b.addr)
	if err != nil {
		return nil, fmt.Errorf("log in to the API at %s: %w", b.addr, err)
	}

	tokens.Store(b.addr, token)

	if r.GetBody != nil {
		body, err := r.GetBody()
		if err != nil {
			return nil, err
		}

		r = r.Clone(r.Context())
		r.Body = body
	}

	return b.send(r, token)
}

func (b bearer) send(r *http.Request, token string) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+token)

	return transport(b.addr).RoundTrip(r)
}

func logIn(ctx context.Context, addr string) (string, error) {
	credentials, _ := json.Marshal(map[string]string{"email": Email, "password": Password})

	token, status, err := post(ctx, addr, "/api/v1/init", credentials)
	if err != nil || status == http.StatusCreated {
		return token, err
	}

	token, status, err = post(ctx, addr, "/api/v1/auth/login", credentials)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("login: status %d", status)
	}

	return token, err
}

func post(ctx context.Context, addr, path string, body []byte) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+path, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}

	res, err := transport(addr).RoundTrip(req)
	if err != nil {
		return "", 0, err
	}

	defer func() { _ = res.Body.Close() }()

	var resp struct {
		Result struct {
			Token string `json:"token"`
		} `json:"result"`
	}

	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return "", res.StatusCode, err
	}

	return resp.Result.Token, res.StatusCode, nil
}
