// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WaitForOIDCProjection polls Zitadel's OIDC authorize endpoint until the given clientID
// is resolvable (i.e. the projection has processed the app creation event).
// On a fresh install, Zitadel's projection workers may take minutes to catch up with the
// event backlog from initial setup. Without this wait, consumer pods restart with a valid
// client ID but Zitadel returns Errors.App.NotFound until the projection is ready.
func (c *Client) WaitForOIDCProjection(clientID string, timeout time.Duration) {
	fmt.Printf("⏳ Waiting for OIDC projection to index client %s (timeout %v)...\n", clientID, timeout)
	deadline := time.Now().Add(timeout)

	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Don't follow redirects — a 302 means the client was found
			return http.ErrUseLastResponse
		},
	}

	authorizeURL := fmt.Sprintf("http://%s/oauth/v2/authorize?client_id=%s&redirect_uri=http://localhost/probe&response_type=code&scope=openid&code_challenge=probe&code_challenge_method=S256",
		c.internalAddr, clientID)

	const pollInterval = 3 * time.Second
	const logInterval = 30 * time.Second
	lastLog := time.Now()

	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, authorizeURL, nil)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}
		// Set Host header to match ExternalDomain so Zitadel resolves the instance
		req.Host = c.domain

		resp, err := httpClient.Do(req)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// 302 = client found, Zitadel redirected to login UI — projection ready.
		// 400 with Errors.App.NotFound = projection hasn't indexed the client yet.
		// 400 with any other reason (e.g. redirect_uri not registered) = client IS
		//   found by the projection; the probe's dummy redirect_uri was rejected after
		//   the client lookup succeeded. Treat this as ready too.
		if resp.StatusCode == http.StatusFound {
			fmt.Printf("✅ OIDC projection ready (client resolvable, got 302)\n")
			return
		}
		if resp.StatusCode == http.StatusBadRequest && !strings.Contains(string(body), "App.NotFound") {
			fmt.Printf("✅ OIDC projection ready (client resolvable, got 400 on redirect_uri validation)\n")
			return
		}

		if time.Since(lastLog) >= logInterval {
			elapsed := time.Since(deadline.Add(-timeout)).Round(time.Second)
			fmt.Printf("   Still waiting for OIDC projection (%v elapsed, status=%d)...\n", elapsed, resp.StatusCode)
			lastLog = time.Now()
		}
		time.Sleep(pollInterval)
	}

	fmt.Printf("⚠️  Timed out waiting for OIDC projection after %v — proceeding anyway\n", timeout)
}
