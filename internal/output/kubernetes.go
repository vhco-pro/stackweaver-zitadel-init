// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package output

import (
	"fmt"
	"os"
	"strings"

	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/config"
	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/k8s"
)

// preserveExisting returns newValue if non-empty, otherwise falls back to the
// existing value from the K8s Secret. This prevents overwriting valid credentials
// with empty strings when Zitadel doesn't return the value on GET (e.g.
// client-secret and webhook signing keys are only returned on first creation).
func preserveExisting(newValue string, existing map[string]string, key string) string {
	if newValue != "" {
		return newValue
	}
	if v, ok := existing[key]; ok && v != "" {
		fmt.Printf("  ℹ️  Preserving existing %q from K8s Secret (not returned by Zitadel on re-run)\n", key)
		return v
	}
	return newValue
}

// WriteKubernetesSecret reads K8S_* env vars and patches the Zitadel
// Secret with the generated credentials. When Zitadel returns an empty value
// for a key (e.g. client-secret on an existing app), the existing value in the
// K8s Secret is preserved — mirroring the Docker Compose path which reads the
// existing .env before writing (see WriteComposeEnv).
func WriteKubernetesSecret(kc *k8s.Client, frontendClientID, apiClientID, apiClientSecret, loginServiceToken, idpSyncKey, complementTokenKey string) error {
	secretName := os.Getenv("K8S_SECRET_NAME")
	namespace := os.Getenv("K8S_NAMESPACE")
	if namespace == "" {
		// Fallback: read from the service account namespace file.
		if b, err := os.ReadFile(k8s.NamespacePath); err == nil {
			namespace = strings.TrimSpace(string(b))
		}
	}
	if secretName == "" || namespace == "" {
		return fmt.Errorf("K8S_SECRET_NAME and K8S_NAMESPACE must be set")
	}

	keyClientID := config.EnvOrDefault("K8S_KEY_CLIENT_ID", "client-id")
	keyClientSecret := config.EnvOrDefault("K8S_KEY_CLIENT_SECRET", "client-secret")
	keyLoginToken := config.EnvOrDefault("K8S_KEY_LOGIN_SERVICE_USER_TOKEN", "login-service-user-token")
	keyFrontendClientID := config.EnvOrDefault("K8S_KEY_FRONTEND_CLIENT_ID", "frontend-client-id")
	keyIdpSyncKey := config.EnvOrDefault("K8S_KEY_WEBHOOK_IDP_SYNC_KEY", "webhook-idp-sync-key")
	keyComplementTokenKey := config.EnvOrDefault("K8S_KEY_WEBHOOK_COMPLEMENT_TOKEN_KEY", "webhook-complement-token-key")

	// Read existing secret values so we can preserve them when Zitadel doesn't
	// return the value (client-secret and webhook signing keys are only returned
	// on first creation, not on subsequent Get calls).
	existing := kc.ReadAllSecretKeys(secretName, namespace)

	data := map[string]string{
		keyClientID:           apiClientID,
		keyClientSecret:       preserveExisting(apiClientSecret, existing, keyClientSecret),
		keyLoginToken:         preserveExisting(loginServiceToken, existing, keyLoginToken),
		keyFrontendClientID:   frontendClientID,
		keyIdpSyncKey:         preserveExisting(idpSyncKey, existing, keyIdpSyncKey),
		keyComplementTokenKey: preserveExisting(complementTokenKey, existing, keyComplementTokenKey),
	}

	if err := kc.PatchSecret(secretName, namespace, data); err != nil {
		return fmt.Errorf("patch zitadel secret %q: %w", secretName, err)
	}
	fmt.Printf("✅ Patched Kubernetes secret %q with Zitadel credentials\n", secretName)
	return nil
}
