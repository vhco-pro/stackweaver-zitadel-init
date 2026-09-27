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
	// Update the Zitadel-managed keys in deploy/.env, which every Compose service
	// reads via env_file: - ./.env. Other entries in the file are preserved.
	deployEnvPath := filepath.Join(projectRoot, "deploy", ".env")
	if err := os.MkdirAll(filepath.Dir(deployEnvPath), 0o755); err != nil {
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

	managed := [][2]string{
		{"ZITADEL_FRONTEND_CLIENT_ID", frontendClientID},
		{"ZITADEL_API_CLIENT_ID", apiClientID},
		{"ZITADEL_API_CLIENT_SECRET", apiClientSecret},
		{"ZITADEL_LOGIN_SERVICE_USER_TOKEN", loginServiceToken},
		{"ZITADEL_LOGIN_SERVICE_USER_ID", loginServiceUserID},
		{"ZITADEL_ISSUER", issuerURL},
		{"ZITADEL_EXTERNAL_HOST", externalHost},
		{"ZITADEL_TLS_MODE", tlsMode},
		{"ZITADEL_WEBHOOK_IDP_SYNC_KEY", idpSyncKey},
		{"ZITADEL_WEBHOOK_COMPLEMENT_TOKEN_KEY", complementTokenKey},
	}

	// Merge rather than overwrite. In the customer Compose bundle this file is
	// the operator's own .env (ENCRYPTION_KEY, POSTGRES_*, VITE_*), mounted into
	// the init container; replacing it wiped those values on every init run.
	existing, err := os.ReadFile(deployEnvPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", deployEnvPath, err)
	}
	if err := os.WriteFile(deployEnvPath, []byte(mergeEnv(string(existing), managed)), 0o644); err != nil {
		return fmt.Errorf("writing deploy/.env: %w", err)
	}
	fmt.Println("✅ Updated the Zitadel keys in deploy/.env (other entries left as they were)")

	// NOTE: Only deploy/.env is written.
	// All docker-compose services use env_file: - ./.env which reads from deploy/.env
	// docker-compose.yml uses ${ZITADEL_ISSUER:-http://localhost:8080} substitution
	// so the issuer URL is automatically propagated to API and frontend.
	// On first run (no .env yet), the fallback http://localhost:8080 is used.
	// After init completes, restart services to pick up the correct issuer.

	return nil
}

// mergeEnv sets each managed KEY=value in an env file's content: a key already
// present is replaced in place (first occurrence; later duplicates are dropped
// so the file stays unambiguous), a missing one is appended. Every other line,
// including comments and blank lines, is kept exactly as it was.
func mergeEnv(existing string, managed [][2]string) string {
	values := make(map[string]string, len(managed))
	for _, kv := range managed {
		values[kv[0]] = kv[1]
	}
	written := make(map[string]bool, len(managed))

	var out []string
	if existing != "" {
		for _, line := range strings.Split(strings.TrimSuffix(existing, "\n"), "\n") {
			key, _, isAssignment := strings.Cut(line, "=")
			key = strings.TrimSpace(key)
			value, isManaged := values[key]
			if !isAssignment || !isManaged || strings.HasPrefix(strings.TrimSpace(line), "#") {
				out = append(out, line)
				continue
			}
			if written[key] {
				continue
			}
			out = append(out, key+"="+value)
			written[key] = true
		}
	}
	for _, kv := range managed {
		if !written[kv[0]] {
			out = append(out, kv[0]+"="+kv[1])
		}
	}
	return strings.Join(out, "\n") + "\n"
}
