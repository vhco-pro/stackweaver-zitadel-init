// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package config

import (
	"strings"
	"testing"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// #829 D2, AC4: SMTP settings come from the environment and a half-configured provider is an
// error rather than a silently unusable Zitadel configuration.
func TestLoadSMTP(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		want      SMTPConfig
		wantErr   []string // substrings the error must contain
		wantNoErr bool
	}{
		{
			name:      "nothing set means no SMTP",
			env:       map[string]string{},
			want:      SMTPConfig{SenderName: "Stackweaver", TLS: true},
			wantNoErr: true,
		},
		{
			name: "full configuration",
			env: map[string]string{
				"SMTP_HOST": "smtp.example.com:587", "SMTP_USER": "resend", "SMTP_PASSWORD": "secret",
				"SMTP_SENDER_ADDRESS": "noreply@mail.example.com", "SMTP_SENDER_NAME": "Example", "SMTP_TLS": "true",
			},
			want: SMTPConfig{
				Host: "smtp.example.com:587", User: "resend", Password: "secret",
				SenderAddress: "noreply@mail.example.com", SenderName: "Example", TLS: true,
			},
			wantNoErr: true,
		},
		{
			name:      "mailpit without auth or TLS",
			env:       map[string]string{"SMTP_HOST": "mailpit:1025", "SMTP_SENDER_ADDRESS": "noreply@stackweaver.local", "SMTP_TLS": "false"},
			want:      SMTPConfig{Host: "mailpit:1025", SenderAddress: "noreply@stackweaver.local", SenderName: "Stackweaver", TLS: false},
			wantNoErr: true,
		},
		{
			name:    "host without sender address names both variables",
			env:     map[string]string{"SMTP_HOST": "smtp.example.com:587"},
			wantErr: []string{"SMTP_HOST", "SMTP_SENDER_ADDRESS"},
		},
		{
			name:    "invalid TLS flag is rejected",
			env:     map[string]string{"SMTP_HOST": "smtp.example.com:587", "SMTP_SENDER_ADDRESS": "a@b.c", "SMTP_TLS": "yes please"},
			wantErr: []string{"SMTP_TLS", `"yes please"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadSMTP(envFrom(tt.env))
			if tt.wantNoErr {
				if err != nil {
					t.Fatalf("LoadSMTP returned error: %v", err)
				}
				if got != tt.want {
					t.Errorf("LoadSMTP = %+v, want %+v", got, tt.want)
				}
				if got.Enabled() != (tt.want.Host != "") {
					t.Errorf("Enabled() = %v for host %q", got.Enabled(), got.Host)
				}
				return
			}
			if err == nil {
				t.Fatalf("LoadSMTP = %+v, want an error", got)
			}
			for _, sub := range tt.wantErr {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q does not mention %s", err, sub)
				}
			}
		})
	}
}

func TestLoadSMTP_StringNeverShowsPassword(t *testing.T) {
	cfg, err := LoadSMTP(envFrom(map[string]string{
		"SMTP_HOST": "smtp.example.com:587", "SMTP_SENDER_ADDRESS": "a@b.c", "SMTP_PASSWORD": "hunter2-secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg.String(), "hunter2-secret") {
		t.Fatalf("String() leaks the password: %s", cfg.String())
	}
}

func TestLoadSelfRegistration(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "false": false, "true": true, "1": true} {
		got, err := LoadSelfRegistration(envFrom(map[string]string{"STACKWEAVER_SELF_REGISTRATION": raw}))
		if err != nil || got != want {
			t.Errorf("LoadSelfRegistration(%q) = %v, %v; want %v, nil", raw, got, err, want)
		}
	}
	_, err := LoadSelfRegistration(envFrom(map[string]string{"STACKWEAVER_SELF_REGISTRATION": "open"}))
	if err == nil || !strings.Contains(err.Error(), "STACKWEAVER_SELF_REGISTRATION") {
		t.Fatalf("an invalid value must be an error naming the variable, got %v", err)
	}
}
