// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvOrDefault returns the environment variable value or the fallback.
func EnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ZitadelInitConfig holds runtime config for the init script (deploy/zitadel-init.yaml).
// More keys can be added here as the init script grows.
type ZitadelInitConfig struct {
	CustomDomains                  []string `yaml:"custom_domains"`
	FrontendRedirectURIs           []string `yaml:"frontend_redirect_uris"`
	FrontendPostLogoutRedirectURIs []string `yaml:"frontend_post_logout_redirect_uris"`
}

// ZitadelDefaultsConfig holds the subset of zitadel-defaults.yaml we need to derive issuer URLs.
type ZitadelDefaultsConfig struct {
	ExternalDomain string `yaml:"ExternalDomain"`
	ExternalPort   int    `yaml:"ExternalPort"`
	ExternalSecure bool   `yaml:"ExternalSecure"`
}

// LoadZitadelDefaults resolves ExternalDomain, ExternalPort, ExternalSecure.
// Precedence: ZITADEL_EXTERNAL* env vars > deploy/zitadel-defaults.yaml > localhost:8080 insecure.
//
// The env override lets the Docker Compose tunnel overlay switch the dev stack to its public
// domain without editing the committed zitadel-defaults.yaml (which stays localhost so plain
// `make up` is platform-agnostic). It uses Zitadel's own env naming (ZITADEL_EXTERNALDOMAIN,
// ZITADEL_EXTERNALPORT, ZITADEL_EXTERNALSECURE) so the same vars configure both the Zitadel
// container and this init step. Kubernetes/Helm sets none of these on zitadel-init, so its
// behavior is unchanged (file/default fallback).
func LoadZitadelDefaults(projectRoot string) ZitadelDefaultsConfig {
	defaults := ZitadelDefaultsConfig{
		ExternalDomain: "localhost",
		ExternalPort:   8080,
		ExternalSecure: false,
	}
	path := filepath.Join(projectRoot, "deploy", "zitadel-defaults.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("⚠️  Could not read %s, using defaults: %v\n", path, err)
	} else {
		var cfg ZitadelDefaultsConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			fmt.Printf("⚠️  Could not parse %s, using defaults: %v\n", path, err)
		} else {
			if cfg.ExternalDomain != "" {
				defaults.ExternalDomain = cfg.ExternalDomain
			}
			if cfg.ExternalPort != 0 {
				defaults.ExternalPort = cfg.ExternalPort
			}
			defaults.ExternalSecure = cfg.ExternalSecure
		}
	}
	// Environment overrides (highest precedence) — Zitadel's native ZITADEL_EXTERNAL* naming.
	if v := os.Getenv("ZITADEL_EXTERNALDOMAIN"); v != "" {
		defaults.ExternalDomain = v
	}
	if v := os.Getenv("ZITADEL_EXTERNALPORT"); v != "" {
		if p, perr := strconv.Atoi(v); perr == nil && p != 0 {
			defaults.ExternalPort = p
		}
	}
	if v := os.Getenv("ZITADEL_EXTERNALSECURE"); v != "" {
		defaults.ExternalSecure = v == "true" || v == "1"
	}
	return defaults
}

// ComputeIssuerURL derives the Zitadel issuer URL from ExternalDomain/Port/Secure.
// Examples:
//   - localhost:8080 insecure  -> http://localhost:8080
//   - zitadel.example.com:443 secure -> https://zitadel.example.com
func ComputeIssuerURL(cfg ZitadelDefaultsConfig) string {
	scheme := "http"
	if cfg.ExternalSecure {
		scheme = "https"
	}
	// Omit port when it's the default for the scheme
	if (cfg.ExternalSecure && cfg.ExternalPort == 443) || (!cfg.ExternalSecure && cfg.ExternalPort == 80) {
		return fmt.Sprintf("%s://%s", scheme, cfg.ExternalDomain)
	}
	return fmt.Sprintf("%s://%s:%d", scheme, cfg.ExternalDomain, cfg.ExternalPort)
}

// LoadZitadelInitConfig reads deploy/zitadel-init.yaml under projectRoot.
// Returns nil if the file is missing or invalid (no error; config is optional).
func LoadZitadelInitConfig(projectRoot string) *ZitadelInitConfig {
	path := filepath.Join(projectRoot, "deploy", "zitadel-init.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg ZitadelInitConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &cfg
}

// LoadCustomDomainsConfig returns custom domains from env ZITADEL_CUSTOM_DOMAINS (comma-separated)
// or from deploy/zitadel-init.yaml (custom_domains list). Env takes precedence.
func LoadCustomDomainsConfig(projectRoot string) []string {
	if env := os.Getenv("ZITADEL_CUSTOM_DOMAINS"); env != "" {
		var out []string
		for _, s := range strings.Split(env, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	cfg := LoadZitadelInitConfig(projectRoot)
	if cfg == nil {
		return nil
	}
	var out []string
	for _, s := range cfg.CustomDomains {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
