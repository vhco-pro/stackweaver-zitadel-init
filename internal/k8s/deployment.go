// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package k8s

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// RestartDeployment triggers a rolling restart by patching the restartedAt
// annotation on the pod template. If Stakater Reloader annotations are present
// on the deployment, the explicit restart is skipped — Reloader will handle it
// automatically when the Secret changes.
func (k *Client) RestartDeployment(deploymentName, namespace, zitadelSecretName string) error {
	url := fmt.Sprintf("%s/apis/apps/v1/namespaces/%s/deployments/%s", k.apiBase, namespace, deploymentName)

	// GET the deployment to inspect Reloader annotations.
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build GET request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+k.token)

	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		fmt.Printf("  ℹ️  deployment %q not found — skipping restart\n", deploymentName)
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("get deployment returned HTTP %d: %s", resp.StatusCode, string(b))
	}

	var deploy struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&deploy); err != nil {
		return fmt.Errorf("decode deployment: %w", err)
	}

	ann := deploy.Metadata.Annotations
	if ann["reloader.stakater.com/auto"] == "true" {
		fmt.Printf("  ℹ️  Reloader auto-watch detected on %q — skipping manual restart\n", deploymentName)
		return nil
	}
	if reload := ann["secret.reloader.stakater.com/reload"]; reload != "" {
		for _, name := range strings.Split(reload, ",") {
			if strings.TrimSpace(name) == zitadelSecretName {
				fmt.Printf("  ℹ️  Reloader secret-watch detected on %q — skipping manual restart\n", deploymentName)
				return nil
			}
		}
	}

	// No Reloader — patch the restartedAt annotation.
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]string{
						"kubectl.kubernetes.io/restartedAt": time.Now().UTC().Format(time.RFC3339),
					},
				},
			},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal restart patch: %w", err)
	}

	req2, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build PATCH request: %w", err)
	}
	req2.Header.Set("Authorization", "Bearer "+k.token)
	req2.Header.Set("Content-Type", "application/merge-patch+json")

	resp2, err := k.http.Do(req2)
	if err != nil {
		return fmt.Errorf("patch deployment: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
		b, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("patch deployment returned HTTP %d: %s", resp2.StatusCode, string(b))
	}
	return nil
}

// RestartConsumers restarts the API, frontend, and login-ui deployments
// (skipping any that aren't found or that Reloader already watches).
func RestartConsumers(k *Client) {
	secretName := os.Getenv("K8S_SECRET_NAME")
	namespace := os.Getenv("K8S_NAMESPACE")

	for _, envVar := range []string{"K8S_API_DEPLOYMENT", "K8S_FRONTEND_DEPLOYMENT", "K8S_LOGIN_UI_DEPLOYMENT"} {
		name := os.Getenv(envVar)
		if name == "" {
			continue
		}
		fmt.Printf("  → restarting %q ...\n", name)
		if err := k.RestartDeployment(name, namespace, secretName); err != nil {
			fmt.Printf("  ⚠️  restart %q: %v\n", name, err)
		}
	}
}
