// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package k8s

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
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
