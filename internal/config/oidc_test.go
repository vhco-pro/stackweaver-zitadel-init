// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package config

import (
	"testing"
	"time"
)

func TestLoadAccessTokenLifetime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "unset defaults to one hour", raw: "", want: time.Hour},
		{name: "a duration", raw: "30m", want: 30 * time.Minute},
		{name: "surrounding space", raw: " 2h ", want: 2 * time.Hour},
		{name: "not a duration", raw: "an hour", wantErr: true},
		{name: "zero", raw: "0s", wantErr: true},
		{name: "negative", raw: "-1h", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoadAccessTokenLifetime(func(key string) string {
				if key == AccessTokenLifetimeVar {
					return tc.raw
				}
				return ""
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LoadAccessTokenLifetime(%q) = %v, want an error", tc.raw, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("LoadAccessTokenLifetime(%q) = %v, %v; want %v", tc.raw, got, err, tc.want)
			}
		})
	}
}
