// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"strings"
	"testing"

	idppb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/idp"
)

// #829 AC25 (Q11): an Azure IdP without a tenant ID would use the "common" tenant, which lets
// any Microsoft account sign in and, with signup open to SSO (Q9), create a verified account.
func TestAzureTenant(t *testing.T) {
	tenant, err := azureTenant("11111111-2222-3333-4444-555555555555", false)
	if err != nil || tenant.GetTenantId() != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("a tenant ID must be used as-is: %v, %v", tenant, err)
	}

	_, err = azureTenant("", false)
	if err == nil || !strings.Contains(err.Error(), "AZURE_AD_TENANT_ID") || !strings.Contains(err.Error(), "AZURE_AD_ALLOW_MULTI_TENANT") {
		t.Fatalf("an empty tenant must be refused, naming both variables: %v", err)
	}

	tenant, err = azureTenant("", true)
	if err != nil || tenant.GetTenantType() != idppb.AzureADTenantType_AZURE_AD_TENANT_TYPE_COMMON {
		t.Fatalf("the explicit opt-in allows the common tenant: %v, %v", tenant, err)
	}
}

// #829 Q9/AC22: an IdP the operator configured may create accounts, which the auth proxy checks
// through Zitadel's creationAllowed filter.
func TestExternalIdPOptionsAllowCreation(t *testing.T) {
	opts := externalIdPOptions()
	if !opts.GetIsCreationAllowed() || !opts.GetIsAutoCreation() {
		t.Fatalf("configured IdPs must allow user creation: %+v", opts)
	}
}
