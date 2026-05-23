// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"fmt"
	"os"
	"strings"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/admin"
	idppb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/idp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// isNotExisting returns true when err is a gRPC "NotFound" indicating
// the upstream record has been deleted while a stale projection still
// reports it. Used for the dead-ID fall-through: zitadel-init's
// findProviderByName can return an IdP ID that was removed from the
// underlying record table but is still attached to a login policy
// (the projection inconsistency observed 2026-05-12). When Update
// against that ID surfaces NotFound, the right move is to fall
// through to Create rather than fail or — worse — silently log
// "Updated" when nothing actually happened.
func isNotExisting(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
		return true
	}
	es := strings.ToLower(err.Error())
	return strings.Contains(es, "notexisting") || strings.Contains(es, "doesn't exist") || strings.Contains(es, "does not exist")
}

// findInstanceProvidersByName searches the DEFAULT LOGIN POLICY's IdP
// attachments for ALL providers matching `name` (case-sensitive exact
// match). Returns the list of provider IDs in projection order (oldest
// first), empty if none.
//
// Why query the login-policy attachments instead of `admin.ListIDPs`:
//   1. `admin.ListIDPs` was observed (2026-05-22) to return only legacy
//      v1-style IdPs and silently miss IdPs created via
//      `AddAzureADProvider` / `AddGenericOIDCProvider` (the v2-style
//      providers we use here). Treating empty as "doesn't exist" caused
//      every zitadel-init re-run to fall through to Create, accumulating
//      duplicates of Microsoft + Okta on every run (4 reruns → 4 dupes).
//   2. The login-policy attachment view is the SAME source of truth the
//      SPA picker uses (via `/v2/settings/login/idps`), so anything
//      visible to the user is visible here, and vice versa. That removes
//      the dual-source-of-truth bug.
//   3. We only care about IdPs that are actually wired into the login
//      flow — any orphan IdP not attached to the policy is invisible
//      and harmless. Looking only at attachments narrows the dedup
//      target to what actually matters.
//
// Returning ALL matches (not just the first) lets the caller sweep
// duplicates created by prior runs — see the dedup branch in the
// Configure*Provider functions.
func (c *Client) findInstanceProvidersByName(name string) ([]string, error) {
	resp, err := c.adminService.ListLoginPolicyIDPs(c.ctx, &admin.ListLoginPolicyIDPsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list login policy IdPs: %w", err)
	}
	if resp == nil {
		return nil, nil
	}
	var ids []string
	for _, link := range resp.GetResult() {
		if link.GetIdpName() == name {
			ids = append(ids, link.GetIdpId())
		}
	}
	return ids, nil
}

// sweepDuplicateIDPs detaches IDPs in `ids` from the default login
// policy and deletes the underlying IdP record. Used by the
// Configure*Provider functions to clean up duplicates created by prior
// runs (see findInstanceProvidersByName for why duplicates accumulated).
// Best-effort: logs and continues on per-ID failure rather than aborting
// the whole init — a stuck duplicate is less harmful than a failed init
// run that leaves the stack half-configured.
func (c *Client) sweepDuplicateIDPs(ids []string, providerName string) {
	for _, id := range ids {
		if _, err := c.adminService.RemoveIDPFromLoginPolicy(c.ctx, &admin.RemoveIDPFromLoginPolicyRequest{
			IdpId: id,
		}); err != nil && !isNotExisting(err) {
			fmt.Printf("⚠️  failed to detach duplicate %s '%s' from login policy: %v\n", providerName, id, err)
		}
		if _, err := c.adminService.RemoveIDP(c.ctx, &admin.RemoveIDPRequest{IdpId: id}); err != nil && !isNotExisting(err) {
			fmt.Printf("⚠️  failed to delete duplicate %s '%s': %v\n", providerName, id, err)
			continue
		}
		fmt.Printf("🧹 Removed duplicate %s IdP: %s\n", providerName, id)
	}
}

// addIDPToDefaultLoginPolicy attaches an instance-level IdP to the
// DEFAULT (instance-level) login policy so the button appears on the
// login screen for every org. Idempotent — "already exists" is treated
// as success. Wave 14: switched from `mgmtService.AddIDPToLoginPolicy`
// (which targeted the org-level custom login policy and required the
// caller's PAT to have org-scoped read access to the IdP) to
// `adminService.AddIDPToLoginPolicy` (instance-level default policy,
// matches the SYSTEM-scope of the auth proxy's PAT). See the
// AC-2/AC-39 Wave-14 root-cause note in the main plan for the full
// rationale.
func (c *Client) addIDPToDefaultLoginPolicy(idpID string) error {
	_, err := c.adminService.AddIDPToLoginPolicy(c.ctx, &admin.AddIDPToLoginPolicyRequest{
		IdpId: idpID,
	})
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "already exists") || strings.Contains(errStr, "alreadyexists") {
			return nil
		}
		return fmt.Errorf("failed to add IdP to default login policy: %w", err)
	}
	return nil
}

// ConfigureAzureADProvider configures Azure AD / Entra ID as an
// INSTANCE-level external identity provider. Instance scope so the
// auth-proxy's SYSTEM-scoped PAT can read the provider when serving
// the unauthenticated IdP picker (Wave 14 root cause for AC-2).
// Skipped if clientID is empty.
func (c *Client) ConfigureAzureADProvider(clientID, clientSecret, tenantID string) error {
	if clientID == "" {
		return nil
	}

	const providerName = "Microsoft"

	tenant := &idppb.AzureADTenant{}
	if tenantID != "" {
		tenant.Type = &idppb.AzureADTenant_TenantId{TenantId: tenantID}
	} else {
		tenant.Type = &idppb.AzureADTenant_TenantType{
			TenantType: idppb.AzureADTenantType_AZURE_AD_TENANT_TYPE_COMMON,
		}
	}
	options := &idppb.Options{
		IsAutoCreation:   true,                                              // JIT provisioning
		IsAutoUpdate:     true,                                              // Sync profile on subsequent logins
		IsLinkingAllowed: true,                                              // Link to existing Zitadel users
		AutoLinking:      idppb.AutoLinkingOption_AUTO_LINKING_OPTION_EMAIL, // Match by email
	}
	scopes := []string{"openid", "profile", "email", "User.Read"}

	// Try Update first if a same-named provider is reported by the
	// projection. If the projection is stale and the record was actually
	// deleted (Wave 14 observed this on a previous-deploy upgrade), the
	// Update will surface NotFound — fall through to Create.
	//
	// If MULTIPLE same-named providers exist (a prior run with the broken
	// name-filter search created duplicates), keep the first, sweep the
	// rest, and Update the survivor.
	existingIDs, err := c.findInstanceProvidersByName(providerName)
	if err != nil {
		return fmt.Errorf("failed to check for existing Azure AD provider: %w", err)
	}
	if len(existingIDs) > 1 {
		fmt.Printf("⚠️  Found %d duplicate Azure AD providers — sweeping %d\n", len(existingIDs), len(existingIDs)-1)
		c.sweepDuplicateIDPs(existingIDs[1:], providerName)
	}
	var existingID string
	if len(existingIDs) > 0 {
		existingID = existingIDs[0]
	}
	if existingID != "" {
		_, updErr := c.adminService.UpdateAzureADProvider(c.ctx, &admin.UpdateAzureADProviderRequest{
			Id:              existingID,
			Name:            providerName,
			ClientId:        clientID,
			ClientSecret:    clientSecret,
			Tenant:          tenant,
			EmailVerified:   true,
			Scopes:          scopes,
			ProviderOptions: options,
		})
		switch {
		case updErr == nil:
			fmt.Printf("✅ Updated Azure AD provider: %s\n", existingID)
			return c.addIDPToDefaultLoginPolicy(existingID)
		case isNotExisting(updErr):
			fmt.Printf("ℹ️  Stale projection entry %s for Azure AD — falling through to Create\n", existingID)
		default:
			return fmt.Errorf("failed to update Azure AD provider: %w", updErr)
		}
	}

	resp, err := c.adminService.AddAzureADProvider(c.ctx, &admin.AddAzureADProviderRequest{
		Name:            providerName,
		ClientId:        clientID,
		ClientSecret:    clientSecret,
		Tenant:          tenant,
		EmailVerified:   true, // Azure AD doesn't send email_verified claim; trust Azure emails
		Scopes:          scopes,
		ProviderOptions: options,
	})
	if err != nil {
		return fmt.Errorf("failed to add Azure AD provider: %w", err)
	}
	idpID := resp.GetId()
	fmt.Printf("✅ Created Azure AD provider (instance): %s\n", idpID)
	return c.addIDPToDefaultLoginPolicy(idpID)
}

// ConfigureGenericOIDCProvider configures a generic OIDC IdP (Okta,
// AWS Cognito, etc.) at INSTANCE level. Same Wave-14 rationale as
// ConfigureAzureADProvider — the picker fetch in the auth proxy uses
// a SYSTEM-scope PAT, so providers MUST live at instance level.
// Skipped if clientID is empty.
func (c *Client) ConfigureGenericOIDCProvider(name, issuer, clientID, clientSecret string) error {
	if clientID == "" {
		return nil
	}
	if name == "" {
		name = "SSO"
	}

	options := &idppb.Options{
		IsAutoCreation:   true,
		IsAutoUpdate:     true,
		IsLinkingAllowed: true,
		AutoLinking:      idppb.AutoLinkingOption_AUTO_LINKING_OPTION_EMAIL,
	}
	scopes := []string{"openid", "profile", "email", "groups"}

	existingIDs, err := c.findInstanceProvidersByName(name)
	if err != nil {
		return fmt.Errorf("failed to check for existing OIDC provider: %w", err)
	}
	if len(existingIDs) > 1 {
		fmt.Printf("⚠️  Found %d duplicate OIDC providers named '%s' — sweeping %d\n", len(existingIDs), name, len(existingIDs)-1)
		c.sweepDuplicateIDPs(existingIDs[1:], name)
	}
	var existingID string
	if len(existingIDs) > 0 {
		existingID = existingIDs[0]
	}
	if existingID != "" {
		_, updErr := c.adminService.UpdateGenericOIDCProvider(c.ctx, &admin.UpdateGenericOIDCProviderRequest{
			Id:               existingID,
			Name:             name,
			Issuer:           issuer,
			ClientId:         clientID,
			ClientSecret:     clientSecret,
			Scopes:           scopes,
			IsIdTokenMapping: false, // Use userinfo endpoint
			ProviderOptions:  options,
		})
		switch {
		case updErr == nil:
			fmt.Printf("✅ Updated Generic OIDC provider '%s': %s\n", name, existingID)
			return c.addIDPToDefaultLoginPolicy(existingID)
		case isNotExisting(updErr):
			fmt.Printf("ℹ️  Stale projection entry %s for OIDC provider '%s' — falling through to Create\n", existingID, name)
		default:
			return fmt.Errorf("failed to update Generic OIDC provider '%s': %w", name, updErr)
		}
	}

	resp, err := c.adminService.AddGenericOIDCProvider(c.ctx, &admin.AddGenericOIDCProviderRequest{
		Name:             name,
		Issuer:           issuer,
		ClientId:         clientID,
		ClientSecret:     clientSecret,
		Scopes:           scopes,
		IsIdTokenMapping: false,
		ProviderOptions:  options,
	})
	if err != nil {
		return fmt.Errorf("failed to add Generic OIDC provider '%s': %w", name, err)
	}
	idpID := resp.GetId()
	fmt.Printf("✅ Created Generic OIDC provider '%s' (instance): %s\n", name, idpID)
	return c.addIDPToDefaultLoginPolicy(idpID)
}

// ConfigureIdentityProviders configures external identity providers based on environment variables.
// This is conditional: only configures providers whose env vars are set.
func (c *Client) ConfigureIdentityProviders() error {
	azureClientID := os.Getenv("AZURE_AD_CLIENT_ID")
	azureClientSecret := os.Getenv("AZURE_AD_CLIENT_SECRET")
	azureTenantID := os.Getenv("AZURE_AD_TENANT_ID")

	oidcName := os.Getenv("OIDC_IDP_NAME")
	oidcIssuer := os.Getenv("OIDC_IDP_ISSUER")
	oidcClientID := os.Getenv("OIDC_IDP_CLIENT_ID")
	oidcClientSecret := os.Getenv("OIDC_IDP_CLIENT_SECRET")

	hasAzure := azureClientID != ""
	hasOIDC := oidcClientID != ""

	if !hasAzure && !hasOIDC {
		fmt.Println("ℹ️  No external IdP environment variables set, skipping IdP configuration")
		return nil
	}

	fmt.Println()
	fmt.Println("--- Configuring External Identity Providers ---")

	if hasAzure {
		if err := c.ConfigureAzureADProvider(azureClientID, azureClientSecret, azureTenantID); err != nil {
			return fmt.Errorf("failed to configure Azure AD provider: %w", err)
		}
	}

	if hasOIDC {
		if err := c.ConfigureGenericOIDCProvider(oidcName, oidcIssuer, oidcClientID, oidcClientSecret); err != nil {
			return fmt.Errorf("failed to configure Generic OIDC provider: %w", err)
		}
	}

	return nil
}
