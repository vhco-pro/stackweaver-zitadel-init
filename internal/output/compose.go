// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package output

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/config"
)

// WriteComposeEnv writes the Docker Compose deploy/.env file with all generated credentials.
func WriteComposeEnv(projectRoot, frontendClientID, apiClientID, apiClientSecret, loginServiceToken, loginServiceUserID, idpSyncKey, complementTokenKey string) error {
	// Write deploy/.env - SINGLE SOURCE OF TRUTH for all environment variables
	// All docker-compose services read from this file via env_file: - ./.env
	deployEnvPath := filepath.Join(projectRoot, "deploy", ".env")
	if err := os.MkdirAll(filepath.Dir(deployEnvPath), 0755); err != nil {
		return fmt.Errorf("failed to create deploy dir: %w", err)
	}

	// If any derived values are empty (Zitadel doesn't return them on GET for
	// existing objects), preserve existing values from the current .env file.
	// This mirrors the K8s path's preserveExisting() logic.
	if apiClientSecret == "" || loginServiceToken == "" || idpSyncKey == "" || complementTokenKey == "" {
		existingEnv, _ := os.ReadFile(deployEnvPath)
		if existingEnv != nil {
			for _, line := range strings.Split(string(existingEnv), "\n") {
				if apiClientSecret == "" && strings.HasPrefix(line, "ZITADEL_API_CLIENT_SECRET=") {
					apiClientSecret = strings.TrimPrefix(line, "ZITADEL_API_CLIENT_SECRET=")
				}
				if loginServiceToken == "" && strings.HasPrefix(line, "ZITADEL_LOGIN_SERVICE_USER_TOKEN=") {
					loginServiceToken = strings.TrimPrefix(line, "ZITADEL_LOGIN_SERVICE_USER_TOKEN=")
				}
				if idpSyncKey == "" && strings.HasPrefix(line, "ZITADEL_WEBHOOK_IDP_SYNC_KEY=") {
					idpSyncKey = strings.TrimPrefix(line, "ZITADEL_WEBHOOK_IDP_SYNC_KEY=")
				}
				if complementTokenKey == "" && strings.HasPrefix(line, "ZITADEL_WEBHOOK_COMPLEMENT_TOKEN_KEY=") {
					complementTokenKey = strings.TrimPrefix(line, "ZITADEL_WEBHOOK_COMPLEMENT_TOKEN_KEY=")
				}
			}
		}
	}

	// Derive issuer URL from zitadel-defaults.yaml (ExternalDomain/Port/Secure)
	zitadelDefaults := config.LoadZitadelDefaults(projectRoot)
	issuerURL := config.ComputeIssuerURL(zitadelDefaults)
	fmt.Printf("ℹ️  Derived issuer URL from zitadel-defaults.yaml: %s\n", issuerURL)

	// External host for the auth-proxy's x-zitadel-instance-host forward - set to
	// the instance's ExternalDomain ALWAYS, including "localhost". On the bridge
	// network the api reaches Zitadel at `zitadel:8080`, so the request Host
	// (`zitadel:8080`) never matches a registered instance domain; Zitadel resolves
	// the instance from this header instead. (Under the old network_mode: host the
	// internal addr was `localhost:8080`, which matched the localhost instance, so
	// the header could be empty - no longer true.) It also makes Zitadel build IdP
	// callback URLs on the right domain.
	externalHost := zitadelDefaults.ExternalDomain

	// Zitadel's --tlsMode must agree with the issuer scheme: `external` (TLS
	// terminated by a proxy → https issuer over a plain-HTTP listener) when secure,
	// `disabled` (http issuer, http listener) for plain-localhost dev. docker-compose.yml
	// reads this as ${ZITADEL_TLS_MODE:-disabled} in the zitadel command.
	tlsMode := "disabled"
	if zitadelDefaults.ExternalSecure {
		tlsMode = "external"
	}

	deployEnv := fmt.Sprintf(`ZITADEL_FRONTEND_CLIENT_ID=%s
ZITADEL_API_CLIENT_ID=%s
ZITADEL_API_CLIENT_SECRET=%s
ZITADEL_LOGIN_SERVICE_USER_TOKEN=%s
ZITADEL_LOGIN_SERVICE_USER_ID=%s
ZITADEL_ISSUER=%s
ZITADEL_EXTERNAL_HOST=%s
ZITADEL_TLS_MODE=%s
ZITADEL_WEBHOOK_IDP_SYNC_KEY=%s
ZITADEL_WEBHOOK_COMPLEMENT_TOKEN_KEY=%s
`, frontendClientID, apiClientID, apiClientSecret, loginServiceToken, loginServiceUserID, issuerURL, externalHost, tlsMode, idpSyncKey, complementTokenKey)

	if err := os.WriteFile(deployEnvPath, []byte(deployEnv), 0644); err != nil {
		return fmt.Errorf("failed to write deploy/.env: %w", err)
	}
	fmt.Println("✅ Wrote deploy/.env (single source of truth for all env vars)")

	// NOTE: Only deploy/.env is written - this is the single source of truth
	// All docker-compose services use env_file: - ./.env which reads from deploy/.env
	// docker-compose.yml uses ${ZITADEL_ISSUER:-http://localhost:8080} substitution
	// so the issuer URL is automatically propagated to API and frontend.
	// On first run (no .env yet), the fallback http://localhost:8080 is used.
	// After init completes, restart services to pick up the correct issuer.

	return nil
}
