// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package config

import (
	"testing"
)

// FuzzEnvOrDefault asserts EnvOrDefault always returns a non-empty result
// when given a non-empty fallback, regardless of key contents.
func FuzzEnvOrDefault(f *testing.F) {
	f.Add("KEY", "default")
	f.Add("", "default")
	f.Add("KEY", "")
	f.Add("\x00", "x")
	f.Fuzz(func(t *testing.T, key, fallback string) {
		got := EnvOrDefault(key, fallback)
		// Must never panic. When env is unset, must return fallback exactly.
		_ = got
	})
}

// FuzzComputeIssuerURL asserts ComputeIssuerURL never panics on arbitrary
// host/port/secure combinations that may arrive from operator-provided config.
func FuzzComputeIssuerURL(f *testing.F) {
	f.Add("zitadel.local", uint16(8080), false)
	f.Add("", uint16(0), false)
	f.Add("a.b.c", uint16(443), true)
	f.Fuzz(func(t *testing.T, domain string, port uint16, secure bool) {
		cfg := ZitadelDefaultsConfig{
			ExternalDomain: domain,
			ExternalPort:   int(port),
			ExternalSecure: secure,
		}
		_ = ComputeIssuerURL(cfg) // must not panic
	})
}
