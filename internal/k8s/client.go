// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package k8s

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// TokenPath is the path to the Kubernetes service account token.
	TokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	// CAPath is the path to the Kubernetes cluster CA certificate.
	CAPath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	// NamespacePath is the path to the Kubernetes service account namespace.
	NamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

const (
	// defaultRetryBudget is the total wall-clock budget for one logical request,
	// comfortably inside the Job's activeDeadlineSeconds.
	defaultRetryBudget = 45 * time.Second
	// defaultRetryBase is the first backoff interval; it doubles per attempt.
	defaultRetryBase = 500 * time.Millisecond
	// maxRetryInterval caps the exponential growth.
	maxRetryInterval = 8 * time.Second
)

// IsRunning checks whether the process is running inside a Kubernetes cluster.
func IsRunning() bool {
	_, err := os.Stat(TokenPath)
	return err == nil
}

// Client holds the shared HTTP client and bearer token for in-cluster API
// calls. Create once with NewClient() and reuse for all operations.
type Client struct {
	http    *http.Client
	token   string
	apiBase string

	// retryBudget / retryBase override the package defaults; zero means default.
	// Only tests set these (to keep the retry suite fast).
	retryBudget time.Duration
	retryBase   time.Duration
}

// NewClient creates a new Kubernetes API client using in-cluster credentials.
func NewClient() (*Client, error) {
	ca, err := os.ReadFile(CAPath)
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)

	token, err := os.ReadFile(TokenPath)
	if err != nil {
		return nil, fmt.Errorf("read service account token: %w", err)
	}

	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
			Timeout:   15 * time.Second,
		},
		token:   strings.TrimSpace(string(token)),
		apiBase: fmt.Sprintf("https://%s:%s", host, port),
	}, nil
}

// apiResult is a fully-read HTTP response from the Kubernetes API server.
type apiResult struct {
	status int
	body   []byte
}

// retryableStatus reports whether an HTTP status code should be retried.
//
// 403 is retried on purpose: RoleBindings created moments earlier (the chart
// applies the secrets-init RBAC at hook weight -5) reach the API server's
// authorization cache asynchronously, so a fresh Job can legitimately observe
// "forbidden" for a second or two. This replaces the `kubectl auth can-i` poll
// loop the previous shell implementation used. Every other 4xx (401, 404, 409,
// 422, …) is a definitive answer and fails immediately.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return code >= 500
}

// do performs an authenticated request against the API server, retrying
// transport errors and retryableStatus responses with exponential backoff until
// the retry budget is exhausted. The response body is fully read and returned;
// a non-nil result means "the API server gave a definitive answer", which the
// callers interpret per status code.
func (k *Client) do(method, url, contentType string, body []byte) (*apiResult, error) {
	budget := k.retryBudget
	if budget <= 0 {
		budget = defaultRetryBudget
	}
	backoff := k.retryBase
	if backoff <= 0 {
		backoff = defaultRetryBase
	}
	deadline := time.Now().Add(budget)

	var lastErr error
	for attempt := 1; ; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, url, reader)
		if err != nil {
			return nil, fmt.Errorf("build %s request: %w", method, err)
		}
		req.Header.Set("Authorization", "Bearer "+k.token)
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := k.http.Do(req)
		switch {
		case err != nil:
			lastErr = fmt.Errorf("%s %s: %w", method, url, err)
		default:
			payload, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case readErr != nil:
				lastErr = fmt.Errorf("%s %s: read response body: %w", method, url, readErr)
			case retryableStatus(resp.StatusCode):
				lastErr = fmt.Errorf("%s %s returned HTTP %d: %s",
					method, url, resp.StatusCode, strings.TrimSpace(string(payload)))
			default:
				return &apiResult{status: resp.StatusCode, body: payload}, nil
			}
		}

		if time.Now().Add(backoff).After(deadline) {
			return nil, fmt.Errorf("giving up after %d attempt(s): %w", attempt, lastErr)
		}
		time.Sleep(backoff)
		if backoff *= 2; backoff > maxRetryInterval {
			backoff = maxRetryInterval
		}
	}
}
