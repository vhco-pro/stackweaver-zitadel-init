// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package config

import (
	"fmt"
	"strings"
	"time"
)

// AccessTokenLifetimeVar names the access-token lifetime zitadel-init holds the instance to. The
// same value seeds DefaultInstance.OIDCSettings for a new instance (deploy/zitadel-defaults.yaml,
// the Helm value zitadel.config.accessTokenLifetime), so one setting drives both.
const AccessTokenLifetimeVar = "ZITADEL_ACCESS_TOKEN_LIFETIME"

// DefaultAccessTokenLifetime is the lifetime used when AccessTokenLifetimeVar is unset.
const DefaultAccessTokenLifetime = time.Hour

// LoadAccessTokenLifetime reads AccessTokenLifetimeVar as a Go duration ("1h", "30m"), defaulting
// to DefaultAccessTokenLifetime. A value that does not parse, or is not positive, is an error
// rather than a silent fallback, so a typo cannot leave tokens at an unintended lifetime.
func LoadAccessTokenLifetime(getenv func(string) string) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(AccessTokenLifetimeVar))
	if raw == "" {
		return DefaultAccessTokenLifetime, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parsing %s %q: %w", AccessTokenLifetimeVar, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s %q must be positive", AccessTokenLifetimeVar, raw)
	}
	return d, nil
}
