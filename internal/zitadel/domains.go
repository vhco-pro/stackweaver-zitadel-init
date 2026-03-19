// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"fmt"
	"strings"

	instanceV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/instance/v2"
)

// EnsureTrustedDomains adds each desired domain as a trusted domain on the instance if not already present.
// This allows Zitadel to accept OIDC requests from both ExternalDomain (e.g. localhost) and the trusted domain(s).
// Uses the Trusted Domains API which requires only iam.write (available to IAM_OWNER),
// unlike Custom Domains which requires system.domain.write (system-level auth).
// Idempotent: skips domains that are already registered.
func (c *Client) EnsureTrustedDomains(domains []string) error {
	if len(domains) == 0 {
		return nil
	}

	instanceSvc := c.api.InstanceServiceV2()

	// List existing trusted domains (no InstanceId -> uses context/host, needs only iam.read)
	listResp, err := instanceSvc.ListTrustedDomains(c.ctx, &instanceV2.ListTrustedDomainsRequest{})
	if err != nil {
		return fmt.Errorf("failed to list trusted domains: %w", err)
	}

	existing := make(map[string]struct{})
	for _, d := range listResp.GetTrustedDomain() {
		if name := d.GetDomain(); name != "" {
			existing[name] = struct{}{}
		}
	}

	for _, domain := range domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		if _, ok := existing[domain]; ok {
			fmt.Printf("✅ Trusted domain already registered: %s\n", domain)
			continue
		}
		// No InstanceId -> uses context/host, needs only iam.write
		_, err := instanceSvc.AddTrustedDomain(c.ctx, &instanceV2.AddTrustedDomainRequest{
			TrustedDomain: domain,
		})
		if err != nil {
			return fmt.Errorf("failed to add trusted domain %q: %w", domain, err)
		}
		fmt.Printf("✅ Added trusted domain: %s\n", domain)
	}

	return nil
}
