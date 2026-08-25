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

// Secret is the subset of a core/v1 Secret this client needs: the name and the
// base64-decoded data map.
type Secret struct {
	Name string
	Data map[string][]byte
}

// GetSecret reads a Secret and reports its existence tri-state:
//
//	(secret, true,  nil)   the Secret exists
//	(nil,    false, nil)   the Secret does not exist (HTTP 404)
//	(nil,    false, err)   the answer is unknown (RBAC, network, malformed body)
//
// The third case is the whole point of the tri-state: a create path must never
// read "I could not tell" as "absent, go ahead and write".
func (k *Client) GetSecret(secretName, namespace string) (*Secret, bool, error) {
	url := fmt.Sprintf("%s/api/v1/namespaces/%s/secrets/%s", k.apiBase, namespace, secretName)
	res, err := k.do(http.MethodGet, url, "", nil)
	if err != nil {
		return nil, false, fmt.Errorf("get secret %q: %w", secretName, err)
	}
	if res.status == http.StatusNotFound {
		return nil, false, nil
	}
	if res.status < 200 || res.status >= 300 {
		return nil, false, fmt.Errorf("get secret %q returned HTTP %d: %s",
			secretName, res.status, strings.TrimSpace(string(res.body)))
	}

	var payload struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(res.body, &payload); err != nil {
		return nil, false, fmt.Errorf("decode secret %q: %w", secretName, err)
	}

	secret := &Secret{Name: secretName, Data: make(map[string][]byte, len(payload.Data))}
	for key, encoded := range payload.Data {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, false, fmt.Errorf("decode key %q of secret %q: %w", key, secretName, err)
		}
		secret.Data[key] = decoded
	}
	return secret, true, nil
}

// ReadAllSecretKeys reads all keys from a Secret and returns them as a decoded,
// whitespace-trimmed string map. A missing Secret yields an empty map and no
// error; any other failure is reported so callers do not mistake "unreadable"
// for "empty".
func (k *Client) ReadAllSecretKeys(secretName, namespace string) (map[string]string, error) {
	secret, found, err := k.GetSecret(secretName, namespace)
	if err != nil {
		return nil, err
	}
	if !found {
		return map[string]string{}, nil
	}
	result := make(map[string]string, len(secret.Data))
	for key, value := range secret.Data {
		result[key] = strings.TrimSpace(string(value))
	}
	return result, nil
}

// ReadSecretKey reads a single key from a Secret.
// Returns an empty string when the Secret or the key is absent.
func (k *Client) ReadSecretKey(secretName, namespace, key string) (string, error) {
	all, err := k.ReadAllSecretKeys(secretName, namespace)
	if err != nil {
		return "", err
	}
	return all[key], nil
}

// PatchSecret patches the named Secret with stringData.
// The secret must already exist (created by secrets-init or supplied by the user).
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
