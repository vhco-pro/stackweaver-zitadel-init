// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package output

import (
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
