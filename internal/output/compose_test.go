// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package output

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeEnv(t *testing.T) {
	managed := [][2]string{
		{"ZITADEL_API_CLIENT_ID", "new-id"},
		{"ZITADEL_ISSUER", "https://auth.example.com"},
	}

	tests := []struct {
		name     string
		existing string
		want     string
	}{
		{
			name:     "empty file gets only the managed keys",
			existing: "",
			want:     "ZITADEL_API_CLIENT_ID=new-id\nZITADEL_ISSUER=https://auth.example.com\n",
		},
		{
			// The customer Compose bundle mounts the operator's own .env here.
			name: "operator values and comments survive, managed keys update in place",
			existing: "# my settings\nENCRYPTION_KEY=abc123\nZITADEL_API_CLIENT_ID=old-id\n\n" +
				"POSTGRES_PASSWORD=s3cret\n",
			want: "# my settings\nENCRYPTION_KEY=abc123\nZITADEL_API_CLIENT_ID=new-id\n\n" +
				"POSTGRES_PASSWORD=s3cret\nZITADEL_ISSUER=https://auth.example.com\n",
		},
		{
			name:     "a commented-out managed key is left alone and the live key appended",
			existing: "# ZITADEL_ISSUER=http://old\nENCRYPTION_KEY=abc123\n",
			want:     "# ZITADEL_ISSUER=http://old\nENCRYPTION_KEY=abc123\nZITADEL_API_CLIENT_ID=new-id\nZITADEL_ISSUER=https://auth.example.com\n",
		},
		{
			name:     "duplicate managed keys collapse to one",
			existing: "ZITADEL_ISSUER=a\nX=1\nZITADEL_ISSUER=b\n",
			want:     "ZITADEL_ISSUER=https://auth.example.com\nX=1\nZITADEL_API_CLIENT_ID=new-id\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mergeEnv(tt.existing, managed); got != tt.want {
				t.Fatalf("mergeEnv() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// #866 AC7: in a Compose run the user stands in the directory holding docker-compose.yml, where
// the file zitadel-init wrote is `.env`. `deploy/.env` is only true inside the container, and
// "restart your services" does not make a container reread an env file.
func TestComposeMessages(t *testing.T) {
	written := ComposeEnvWrittenMessage()
	if strings.Contains(written, "deploy/") {
		t.Errorf("the written-file message names a path the user does not have: %q", written)
	}
	if !strings.Contains(written, ".env") {
		t.Errorf("the written-file message must name .env: %q", written)
	}

	closing := ComposeClosingMessage()
	if strings.Contains(strings.ToLower(closing), "restart your services") {
		t.Errorf("the closing message tells the user to restart, which does not reread an env file: %q", closing)
	}
	if !strings.Contains(closing, "docker compose up -d") {
		t.Errorf("the closing message must name the command that applies the values: %q", closing)
	}
}

func readEnvFile(t *testing.T, root string) map[string]string {
	t.Helper()
	return readEnv(t, filepath.Join(root, "deploy", ".env"))
}

func readEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

// The BFF client id and secret are written where the API reads them, and an empty secret (what
// Zitadel returns for an app that already exists) keeps the one already stored.
func TestWriteComposeEnv_BFFClient(t *testing.T) {
	root := t.TempDir()

	envPath := filepath.Join(root, "deploy", ".env")
	if err := WriteComposeEnv(envPath, root, "api", "api-secret", "login-tok", "login-user", "idp", "comp",
		BFFClient{ClientID: "bff-id", ClientSecret: "bff-secret"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	env := readEnvFile(t, root)
	if env["ZITADEL_BFF_CLIENT_ID"] != "bff-id" || env["ZITADEL_BFF_CLIENT_SECRET"] != "bff-secret" {
		t.Fatalf("BFF keys not written: id=%q secret=%q", env["ZITADEL_BFF_CLIENT_ID"], env["ZITADEL_BFF_CLIENT_SECRET"])
	}
	if !HasStoredComposeBFFSecret(envPath) {
		t.Fatalf("HasStoredComposeBFFSecret must report the stored secret")
	}

	if err := WriteComposeEnv(envPath, root, "api", "", "", "login-user", "", "",
		BFFClient{ClientID: "bff-id"}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if got := readEnvFile(t, root)["ZITADEL_BFF_CLIENT_SECRET"]; got != "bff-secret" {
		t.Fatalf("an empty secret must preserve the stored one, got %q", got)
	}
}

func TestHasStoredComposeBFFSecret_NoFile(t *testing.T) {
	if HasStoredComposeBFFSecret(filepath.Join(t.TempDir(), "deploy", ".env")) {
		t.Fatalf("no .env means no stored secret")
	}
}

// The Compose env file is <PROJECT_ROOT>/deploy/.env unless ZITADEL_INIT_ENV_FILE names another
// path, which lets a Compose slot keep its generated values outside the checkout.
func TestComposeEnvPath(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if got := ComposeEnvPath("/config", getenv); got != "/config/deploy/.env" {
		t.Errorf("default path = %q, want /config/deploy/.env", got)
	}
	env["ZITADEL_INIT_ENV_FILE"] = "/swc-state/.env"
	if got := ComposeEnvPath("/config", getenv); got != "/swc-state/.env" {
		t.Errorf("override = %q, want /swc-state/.env", got)
	}
}

// Each secret is on disk as soon as it is recorded, and a later record with an empty value (what
// Zitadel returns for an object that already exists) never replaces it.
func TestUpdateComposeEnv_PersistsEachWriteAndKeepsStoredSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("# operator\nENCRYPTION_KEY=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := UpdateComposeEnv(path, [][2]string{{"ZITADEL_API_CLIENT_ID", "api"}, {"ZITADEL_API_CLIENT_SECRET", "api-secret"}}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if got := readEnv(t, path)["ZITADEL_API_CLIENT_SECRET"]; got != "api-secret" {
		t.Fatalf("the API secret is not on disk after its own write: %q", got)
	}

	if err := UpdateComposeEnv(path, [][2]string{{"ZITADEL_API_CLIENT_SECRET", ""}, {"ZITADEL_LOGIN_SERVICE_USER_TOKEN", "tok"}}); err != nil {
		t.Fatalf("second update: %v", err)
	}
	env := readEnv(t, path)
	if env["ZITADEL_API_CLIENT_SECRET"] != "api-secret" {
		t.Errorf("an empty value replaced the stored secret: %q", env["ZITADEL_API_CLIENT_SECRET"])
	}
	if env["ZITADEL_LOGIN_SERVICE_USER_TOKEN"] != "tok" || env["ENCRYPTION_KEY"] != "abc" {
		t.Errorf("want the new token and the operator value kept, got %v", env)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the file mode must be kept across the replace, got %v (%v)", fi.Mode().Perm(), err)
	}
}

// A write that fails before it lands leaves the existing file exactly as it was, and no temporary
// file behind: the replace is a rename, never a truncate-and-rewrite.
func TestUpdateComposeEnv_FailedWriteLeavesTheFileIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	before := "ZITADEL_API_CLIENT_SECRET=api-secret\nENCRYPTION_KEY=abc\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	orig := renameFile
	renameFile = func(string, string) error { return errors.New("injected rename failure") }
	defer func() { renameFile = orig }()

	if err := UpdateComposeEnv(path, [][2]string{{"ZITADEL_BFF_CLIENT_SECRET", "bff"}}); err == nil {
		t.Fatal("want the injected failure reported")
	}
	got, err := os.ReadFile(filepath.Clean(path))
	if err != nil || string(got) != before {
		t.Fatalf("the existing file changed on a failed write: %q (%v)", got, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("a temporary file was left behind: %v", entries)
	}
}

// A missing file is created; nothing is required to exist first.
func TestUpdateComposeEnv_CreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploy", ".env")
	if err := UpdateComposeEnv(path, [][2]string{{"ZITADEL_BFF_CLIENT_ID", "bff"}}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := readEnv(t, path)["ZITADEL_BFF_CLIENT_ID"]; got != "bff" {
		t.Fatalf("got %q", got)
	}
}
