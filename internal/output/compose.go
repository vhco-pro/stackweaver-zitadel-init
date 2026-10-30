// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package output

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/config"
)

// ComposeEnvWrittenMessage reports the file WriteComposeEnv updated, by the name it has where the
// user ran Compose: `.env` beside docker-compose.yml. Inside this container the same file is
// <PROJECT_ROOT>/deploy/.env, a path the user does not have.
func ComposeEnvWrittenMessage() string {
	return "✅ Updated the Zitadel keys in .env, beside your docker-compose.yml (other entries left as they were)"
}

// ComposeClosingMessage tells the user how the generated values reach services that were created
// before this run. Compose reads .env when it creates a container, so a restart is not enough;
// `docker compose up -d` recreates the services whose configuration changed.
func ComposeClosingMessage() string {
	return "Configuration written. Services that were already running before this run do not have the new values yet: apply them with `docker compose up -d`."
}

// BFFClient is the confidential client the API uses to complete browser sign-in. ClientSecret is
// empty when Zitadel did not return one (an app that already existed), and the writers then keep
// the stored secret.
type BFFClient struct {
	ClientID     string
	ClientSecret string
}

// composeBFFSecretKey is the deploy/.env key holding the BFF client secret.
const composeBFFSecretKey = "ZITADEL_BFF_CLIENT_SECRET"

// ComposeEnvPath is the file the Compose run keeps its generated values in:
// <PROJECT_ROOT>/deploy/.env, or ZITADEL_INIT_ENV_FILE when that names another path. A Compose
// slot (scripts/compose-slot.sh) uses the override to keep a slot's values outside the checkout.
func ComposeEnvPath(projectRoot string, getenv func(string) string) string {
	if p := strings.TrimSpace(getenv("ZITADEL_INIT_ENV_FILE")); p != "" {
		return p
	}
	return filepath.Join(projectRoot, "deploy", ".env")
}

// HasStoredComposeBFFSecret reports whether the env file already holds a BFF client secret, so an
// existing BFF app is not given a new secret on every run.
func HasStoredComposeBFFSecret(envPath string) bool {
	b, err := os.ReadFile(filepath.Clean(envPath))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, composeBFFSecretKey+"="); ok && v != "" {
			return true
		}
	}
	return false
}

// renameFile is os.Rename, replaceable in tests to fail the last step of a write.
var renameFile = os.Rename

// UpdateComposeEnv sets managed KEY=value pairs in the env file and leaves every other line as it
// was. An empty value never replaces a stored one: Zitadel returns a secret only when it creates
// the object, so a run that finds the object existing passes "" and the stored secret stays.
//
// Each call is one atomic replace (a temporary file in the same directory, synced, then renamed
// over the original), so the provisioning flow can record every secret the moment Zitadel returns
// it, and a write that fails, or a run killed mid-write, leaves the previous file whole. The file
// keeps its permissions. Note the replace gives the file a new inode: a container that bind-mounts
// the file itself (not its directory) sees the version that existed when it started.
func UpdateComposeEnv(envPath string, managed [][2]string) error {
	existing, err := os.ReadFile(filepath.Clean(envPath))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", envPath, err)
	}
	stored := map[string]string{}
	for _, line := range strings.Split(string(existing), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			if _, seen := stored[strings.TrimSpace(k)]; !seen {
				stored[strings.TrimSpace(k)] = v
			}
		}
	}
	merged := make([][2]string, 0, len(managed))
	for _, kv := range managed {
		if kv[1] == "" && stored[kv[0]] != "" {
			kv[1] = stored[kv[0]]
		}
		merged = append(merged, kv)
	}
	return writeFileAtomic(envPath, []byte(mergeEnv(string(existing), merged)))
}

func writeFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	mode := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file beside %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err = renameFile(tmp.Name(), path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	if d, openErr := os.Open(filepath.Clean(dir)); openErr == nil {
		_ = d.Sync() // make the rename durable; a failure here does not undo it
		_ = d.Close()
	}
	return nil
}

// WriteComposeEnv writes the full set of generated values to the Compose env file at the end of a
// run: the credentials (each already recorded as it was created, see UpdateComposeEnv) and the
// issuer, external host and TLS mode derived from zitadel-defaults.yaml under projectRoot.
func WriteComposeEnv(envPath, projectRoot, apiClientID, apiClientSecret, loginServiceToken, loginServiceUserID, idpSyncKey, complementTokenKey string, bff BFFClient) error {
	// Derive issuer URL from zitadel-defaults.yaml (ExternalDomain/Port/Secure)
	zitadelDefaults := config.LoadZitadelDefaults(projectRoot)
	issuerURL := config.ComputeIssuerURL(zitadelDefaults)
	fmt.Printf("ℹ️  Derived issuer URL from zitadel-defaults.yaml: %s\n", issuerURL)

	// External host for the auth-proxy's x-zitadel-instance-host forward - set to
	// the instance's ExternalDomain ALWAYS, including "localhost". On the bridge
	// network the api reaches Zitadel at `zitadel:8080`, so the request Host
	// (`zitadel:8080`) never matches a registered instance domain; Zitadel resolves
	// the instance from this header instead. It also makes Zitadel build IdP
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

	// Merge rather than overwrite. In the customer Compose bundle this file is
	// the operator's own .env (ENCRYPTION_KEY, POSTGRES_*, VITE_*), mounted into
	// the init container; replacing it wiped those values on every init run.
	// An empty derived value (Zitadel does not return secrets for existing
	// objects) keeps the stored one.
	if err := UpdateComposeEnv(envPath, [][2]string{
		{"ZITADEL_API_CLIENT_ID", apiClientID},
		{"ZITADEL_API_CLIENT_SECRET", apiClientSecret},
		{"ZITADEL_LOGIN_SERVICE_USER_TOKEN", loginServiceToken},
		{"ZITADEL_LOGIN_SERVICE_USER_ID", loginServiceUserID},
		{"ZITADEL_ISSUER", issuerURL},
		{"ZITADEL_EXTERNAL_HOST", externalHost},
		{"ZITADEL_TLS_MODE", tlsMode},
		{"ZITADEL_WEBHOOK_IDP_SYNC_KEY", idpSyncKey},
		{"ZITADEL_WEBHOOK_COMPLEMENT_TOKEN_KEY", complementTokenKey},
		{"ZITADEL_BFF_CLIENT_ID", bff.ClientID},
		{composeBFFSecretKey, bff.ClientSecret},
	}); err != nil {
		return err
	}
	fmt.Println(ComposeEnvWrittenMessage())
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
