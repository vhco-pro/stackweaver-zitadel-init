// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"fmt"
	"os"
	"strings"

	idppb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/idp"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	objectpb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object"
)

// findProviderByName searches for an existing IdP provider by name.
// Returns the provider ID if found, empty string if not found.
func (c *Client) findProviderByName(name string) (string, error) {
	resp, err := c.mgmtService.ListProviders(c.ctx, &management.ListProvidersRequest{
		Queries: []*management.ProviderQuery{
			{
				Query: &management.ProviderQuery_IdpNameQuery{
					IdpNameQuery: &idppb.IDPNameQuery{
						Name:   name,
						Method: objectpb.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS,
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to list providers: %w", err)
	}
	if resp != nil && len(resp.GetResult()) > 0 {
		return resp.GetResult()[0].GetId(), nil
	}
	return "", nil
}

// ensureCustomLoginPolicy ensures the organization has a custom login policy with AllowExternalIdp enabled.
// Zitadel requires a custom (org-level) login policy before IdPs can be added to it.
// If the org only inherits the default instance policy, this creates a custom one.
func (c *Client) ensureCustomLoginPolicy() error {
	// Get the current login policy (may be inherited default or custom)
	resp, err := c.mgmtService.GetLoginPolicy(c.ctx, &management.GetLoginPolicyRequest{})
	if err != nil {
		return fmt.Errorf("failed to get login policy: %w", err)
	}

	p := resp.GetPolicy()
	if p != nil && !p.GetIsDefault() {
		// Org already has a custom login policy — just ensure AllowExternalIdp is true
		if !p.GetAllowExternalIdp() {
			_, err := c.mgmtService.UpdateCustomLoginPolicy(c.ctx, &management.UpdateCustomLoginPolicyRequest{
				AllowUsernamePassword:      p.GetAllowUsernamePassword(),
				AllowRegister:              p.GetAllowRegister(),
				AllowExternalIdp:           true,
				ForceMfa:                   p.GetForceMfa(),
				PasswordlessType:           p.GetPasswordlessType(),
				HidePasswordReset:          p.GetHidePasswordReset(),
				PasswordCheckLifetime:      p.GetPasswordCheckLifetime(),
				ExternalLoginCheckLifetime: p.GetExternalLoginCheckLifetime(),
				MfaInitSkipLifetime:        p.GetMfaInitSkipLifetime(),
				SecondFactorCheckLifetime:  p.GetSecondFactorCheckLifetime(),
				MultiFactorCheckLifetime:   p.GetMultiFactorCheckLifetime(),
				AllowDomainDiscovery:       p.GetAllowDomainDiscovery(),
				DisableLoginWithEmail:      p.GetDisableLoginWithEmail(),
				DisableLoginWithPhone:      p.GetDisableLoginWithPhone(),
				ForceMfaLocalOnly:          p.GetForceMfaLocalOnly(),
			})
			if err != nil {
				return fmt.Errorf("failed to update login policy to allow external IdP: %w", err)
			}
			fmt.Println("✅ Updated org login policy: AllowExternalIdp=true")
		}
		return nil
	}

	// No custom policy — create one based on the default, with AllowExternalIdp enabled
	allowUsername := true
	allowRegister := true
	if p != nil {
		allowUsername = p.GetAllowUsernamePassword()
		allowRegister = p.GetAllowRegister()
	}

	_, err = c.mgmtService.AddCustomLoginPolicy(c.ctx, &management.AddCustomLoginPolicyRequest{
		AllowUsernamePassword: allowUsername,
		AllowRegister:         allowRegister,
		AllowExternalIdp:      true,
	})
	if err != nil {
		// Ignore "already exists" — another process may have created it
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "already exists") || strings.Contains(errStr, "alreadyexists") {
			return nil
		}
		return fmt.Errorf("failed to create custom login policy: %w", err)
	}
	fmt.Println("✅ Created org-level login policy with AllowExternalIdp=true")
	return nil
}

// addIDPToLoginPolicy adds an IdP to the organization's login policy so it appears on the login screen.
// Ensures the org has a custom login policy first (Zitadel requires this).
func (c *Client) addIDPToLoginPolicy(idpID string) error {
	// Ensure org-level login policy exists before adding IdP
	if err := c.ensureCustomLoginPolicy(); err != nil {
		return err
	}

	_, err := c.mgmtService.AddIDPToLoginPolicy(c.ctx, &management.AddIDPToLoginPolicyRequest{
		IdpId:     idpID,
		OwnerType: idppb.IDPOwnerType_IDP_OWNER_TYPE_ORG,
	})
	if err != nil {
		// Ignore "already exists" errors (idempotent)
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "already exists") || strings.Contains(errStr, "alreadyexists") {
			return nil
		}
		return fmt.Errorf("failed to add IdP to login policy: %w", err)
	}
	return nil
}

// ConfigureAzureADProvider configures Azure AD / Entra ID as an external identity provider.
// Uses the dedicated AddAzureADProvider template in the Zitadel SDK.
// Skipped if clientID is empty.
func (c *Client) ConfigureAzureADProvider(clientID, clientSecret, tenantID string) error {
	if clientID == "" {
		return nil
	}

	providerName := "Microsoft"

	// Check if provider already exists
	existingID, err := c.findProviderByName(providerName)
	if err != nil {
		return fmt.Errorf("failed to check for existing Azure AD provider: %w", err)
	}
	if existingID != "" {
		// Update the existing provider with current configuration
		tenant := &idppb.AzureADTenant{}
		if tenantID != "" {
			tenant.Type = &idppb.AzureADTenant_TenantId{TenantId: tenantID}
		} else {
			tenant.Type = &idppb.AzureADTenant_TenantType{
				TenantType: idppb.AzureADTenantType_AZURE_AD_TENANT_TYPE_COMMON,
			}
		}
		_, err := c.mgmtService.UpdateAzureADProvider(c.ctx, &management.UpdateAzureADProviderRequest{
			Id:            existingID,
			Name:          providerName,
			ClientId:      clientID,
			ClientSecret:  clientSecret,
			Tenant:        tenant,
			EmailVerified: true,
			Scopes:        []string{"openid", "profile", "email", "User.Read"},
			ProviderOptions: &idppb.Options{
				IsAutoCreation:   true,
				IsAutoUpdate:     true,
				IsLinkingAllowed: true,
				AutoLinking:      idppb.AutoLinkingOption_AUTO_LINKING_OPTION_EMAIL,
			},
		})
		if err != nil {
			return fmt.Errorf("failed to update Azure AD provider: %w", err)
		}
		fmt.Printf("✅ Updated Azure AD provider: %s\n", existingID)
		// Ensure it's in the login policy
		if err := c.addIDPToLoginPolicy(existingID); err != nil {
			return err
		}
		return nil
	}

	// Configure Azure AD tenant
	tenant := &idppb.AzureADTenant{}
	if tenantID != "" {
		tenant.Type = &idppb.AzureADTenant_TenantId{TenantId: tenantID}
	} else {
		tenant.Type = &idppb.AzureADTenant_TenantType{
			TenantType: idppb.AzureADTenantType_AZURE_AD_TENANT_TYPE_COMMON,
		}
	}

	resp, err := c.mgmtService.AddAzureADProvider(c.ctx, &management.AddAzureADProviderRequest{
		Name:          providerName,
		ClientId:      clientID,
		ClientSecret:  clientSecret,
		Tenant:        tenant,
		EmailVerified: true, // Azure AD doesn't send email_verified claim; trust Azure emails
		Scopes:        []string{"openid", "profile", "email", "User.Read"},
		ProviderOptions: &idppb.Options{
			IsAutoCreation:   true,                                              // JIT provisioning
			IsAutoUpdate:     true,                                              // Sync profile on subsequent logins
			IsLinkingAllowed: true,                                              // Link to existing Zitadel users
			AutoLinking:      idppb.AutoLinkingOption_AUTO_LINKING_OPTION_EMAIL, // Match by email
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add Azure AD provider: %w", err)
	}

	idpID := resp.GetId()
	fmt.Printf("✅ Created Azure AD provider: %s\n", idpID)

	// Add to login policy so button appears on login screen
	if err := c.addIDPToLoginPolicy(idpID); err != nil {
		return err
	}
	fmt.Println("✅ Added Azure AD provider to login policy")

	return nil
}

// ConfigureGenericOIDCProvider configures a generic OIDC identity provider (Okta, AWS Cognito, etc.).
// Uses the AddGenericOIDCProvider API.
// Skipped if clientID is empty.
func (c *Client) ConfigureGenericOIDCProvider(name, issuer, clientID, clientSecret string) error {
	if clientID == "" {
		return nil
	}

	if name == "" {
		name = "SSO"
	}

	// Check if provider already exists
	existingID, err := c.findProviderByName(name)
	if err != nil {
		return fmt.Errorf("failed to check for existing OIDC provider: %w", err)
	}
	if existingID != "" {
		// Update the existing provider with current configuration
		// This ensures changes to issuer, client ID, or client secret are applied
		_, err := c.mgmtService.UpdateGenericOIDCProvider(c.ctx, &management.UpdateGenericOIDCProviderRequest{
			Id:               existingID,
			Name:             name,
			Issuer:           issuer,
			ClientId:         clientID,
			ClientSecret:     clientSecret,
			Scopes:           []string{"openid", "profile", "email", "groups"},
			IsIdTokenMapping: false,
			ProviderOptions: &idppb.Options{
				IsAutoCreation:   true,
				IsAutoUpdate:     true,
				IsLinkingAllowed: true,
				AutoLinking:      idppb.AutoLinkingOption_AUTO_LINKING_OPTION_EMAIL,
			},
		})
		if err != nil {
			return fmt.Errorf("failed to update Generic OIDC provider '%s': %w", name, err)
		}
		fmt.Printf("✅ Updated Generic OIDC provider '%s': %s\n", name, existingID)
		// Ensure it's in the login policy
		if err := c.addIDPToLoginPolicy(existingID); err != nil {
			return err
		}
		return nil
	}

	resp, err := c.mgmtService.AddGenericOIDCProvider(c.ctx, &management.AddGenericOIDCProviderRequest{
		Name:             name,
		Issuer:           issuer,
		ClientId:         clientID,
		ClientSecret:     clientSecret,
		Scopes:           []string{"openid", "profile", "email", "groups"},
		IsIdTokenMapping: false, // Use userinfo endpoint
		ProviderOptions: &idppb.Options{
			IsAutoCreation:   true,
			IsAutoUpdate:     true,
			IsLinkingAllowed: true,
			AutoLinking:      idppb.AutoLinkingOption_AUTO_LINKING_OPTION_EMAIL,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add Generic OIDC provider '%s': %w", name, err)
	}

	idpID := resp.GetId()
	fmt.Printf("✅ Created Generic OIDC provider '%s': %s\n", name, idpID)

	// Add to login policy
	if err := c.addIDPToLoginPolicy(idpID); err != nil {
		return err
	}
	fmt.Printf("✅ Added Generic OIDC provider '%s' to login policy\n", name)

	return nil
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
