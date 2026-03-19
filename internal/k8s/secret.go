// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package k8s

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ReadAllSecretKeys reads all keys from a Kubernetes Secret and returns them as
// a decoded map. Returns an empty map (not nil) on any error or if the secret
// has no data, so callers can safely index without nil checks.
func (k *Client) ReadAllSecretKeys(secretName, namespace string) map[string]string {
	url := fmt.Sprintf("%s/api/v1/namespaces/%s/secrets/%s", k.apiBase, namespace, secretName)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return map[string]string{}
	}
	req.Header.Set("Authorization", "Bearer "+k.token)

	resp, err := k.http.Do(req)
	if err != nil {
		return map[string]string{}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return map[string]string{}
	}

	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&secret); err != nil {
		return map[string]string{}
	}

	result := make(map[string]string, len(secret.Data))
	for k, v := range secret.Data {
		decoded, err := base64.StdEncoding.DecodeString(v)
		if err == nil {
			result[k] = strings.TrimSpace(string(decoded))
		}
	}
	return result
}

// ReadSecretKey reads a single key from a Kubernetes Secret.
// Returns the decoded value, or empty string if the key is missing or empty.
func (k *Client) ReadSecretKey(secretName, namespace, key string) (string, error) {
	all := k.ReadAllSecretKeys(secretName, namespace)
	if v, ok := all[key]; ok && v != "" {
		return v, nil
	}
	return "", nil
}

// PatchSecret patches the named Secret with stringData.
// The secret must already exist (created by the Helm chart).
func (k *Client) PatchSecret(secretName, namespace string, data map[string]string) error {
	body, err := json.Marshal(map[string]any{"stringData": data})
	if err != nil {
		return fmt.Errorf("marshal secret patch: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/namespaces/%s/secrets/%s", k.apiBase, namespace, secretName)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/merge-patch+json")

	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("patch secret: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("patch secret returned HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
