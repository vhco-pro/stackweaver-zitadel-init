// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"fmt"

	applicationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/application/v2"
)

// GetOrCreateFrontendApp finds or creates the frontend OIDC application.
func (c *Client) GetOrCreateFrontendApp(orgID, projectID string, extraRedirectURIs, extraPostLogoutURIs []string) (string, error) {
	// Build the full list of redirect URIs (localhost + any extras from config)
	redirectURIs := []string{"http://localhost:5173/auth/callback"}
	redirectURIs = append(redirectURIs, extraRedirectURIs...)
	postLogoutURIs := []string{"http://localhost:5173"}
	postLogoutURIs = append(postLogoutURIs, extraPostLogoutURIs...)

	// Try to find existing app
	listResp, err := c.applicationService.ListApplications(c.ctx, &applicationV2.ListApplicationsRequest{
		Filters: []*applicationV2.ApplicationSearchFilter{
			{
				Filter: &applicationV2.ApplicationSearchFilter_ProjectIdFilter{
					ProjectIdFilter: &applicationV2.ProjectIDFilter{
						ProjectId: projectID,
					},
				},
			},
			{
				Filter: &applicationV2.ApplicationSearchFilter_NameFilter{
					NameFilter: &applicationV2.ApplicationNameFilter{
						Name: "IAC Platform Frontend",
					},
				},
			},
		},
	})
	if err == nil && listResp != nil && len(listResp.GetApplications()) > 0 {
		appID := listResp.GetApplications()[0].GetApplicationId()

		// Get the app details to retrieve the ClientId from OIDC configuration
		getResp, err := c.applicationService.GetApplication(c.ctx, &applicationV2.GetApplicationRequest{
			ApplicationId: appID,
		})
		if err == nil && getResp != nil {
			if app := getResp.GetApplication(); app != nil {
				if oidcConfig := app.GetOidcConfiguration(); oidcConfig != nil {
					clientID := oidcConfig.GetClientId()
					if clientID != "" {
						fmt.Printf("✅ Using existing frontend app: %s (ClientId: %s)\n", appID, clientID)

						// Ensure redirect URIs are up to date
						if err := c.ensureFrontendRedirectURIs(appID, projectID, oidcConfig, redirectURIs, postLogoutURIs); err != nil {
							fmt.Printf("⚠️  Warning: could not update redirect URIs: %v\n", err)
						}

						return clientID, nil
					}
				}
			}
		}

		// Fall back to ApplicationId if ClientId not found
		fmt.Printf("✅ Using existing frontend app: %s\n", appID)
		return appID, nil
	}

	// Create new OIDC app
	createResp, err := c.applicationService.CreateApplication(c.ctx, &applicationV2.CreateApplicationRequest{
		ProjectId: projectID,
		Name:      "IAC Platform Frontend",
		ApplicationType: &applicationV2.CreateApplicationRequest_OidcConfiguration{
			OidcConfiguration: &applicationV2.CreateOIDCApplicationRequest{
				RedirectUris:  redirectURIs,
				ResponseTypes: []applicationV2.OIDCResponseType{
					applicationV2.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE,
				},
				GrantTypes: []applicationV2.OIDCGrantType{
					applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE,
					applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_REFRESH_TOKEN,
				},
				ApplicationType: applicationV2.OIDCApplicationType_OIDC_APP_TYPE_WEB,
				AuthMethodType:  applicationV2.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_NONE,
				AccessTokenType: applicationV2.OIDCTokenType_OIDC_TOKEN_TYPE_JWT,
				PostLogoutRedirectUris: postLogoutURIs,
				DevelopmentMode: true,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create frontend app: %w", err)
	}

	appID := createResp.GetApplicationId()

	// Get the app to retrieve ClientId (V2 API reads from event store, works immediately)
	getResp, err := c.applicationService.GetApplication(c.ctx, &applicationV2.GetApplicationRequest{
		ApplicationId: appID,
	})
	if err != nil {
		// If GetApplication fails, fall back to using ApplicationId
		fmt.Printf("⚠️  Warning: Could not retrieve app details, using ApplicationId: %s\n", appID)
	return appID, nil
	}

	// Extract ClientId from OIDC configuration
	var clientID string
	if app := getResp.GetApplication(); app != nil {
		if oidcConfig := app.GetOidcConfiguration(); oidcConfig != nil {
			clientID = oidcConfig.GetClientId()
		}
	}

	if clientID == "" {
		// Fall back to ApplicationId if ClientId not found
		clientID = appID
		fmt.Printf("⚠️  Warning: ClientId not found in OIDC config, using ApplicationId: %s\n", appID)
	} else {
		fmt.Printf("✅ Created frontend app: %s (ClientId: %s)\n", appID, clientID)
	}

	return clientID, nil
}

// ensureFrontendRedirectURIs checks if the existing app's redirect URIs contain all desired URIs.
// If any are missing, it calls UpdateApplication to add them.
func (c *Client) ensureFrontendRedirectURIs(appID, projectID string, oidcConfig *applicationV2.OIDCConfiguration, wantRedirect, wantPostLogout []string) error {
	existingRedirect := oidcConfig.GetRedirectUris()
	existingPostLogout := oidcConfig.GetPostLogoutRedirectUris()

	// Check if all desired URIs are already present
	redirectSet := make(map[string]struct{}, len(existingRedirect))
	for _, u := range existingRedirect {
		redirectSet[u] = struct{}{}
	}
	postLogoutSet := make(map[string]struct{}, len(existingPostLogout))
	for _, u := range existingPostLogout {
		postLogoutSet[u] = struct{}{}
	}

	needsUpdate := false
	mergedRedirect := existingRedirect
	for _, u := range wantRedirect {
		if _, ok := redirectSet[u]; !ok {
			mergedRedirect = append(mergedRedirect, u)
			needsUpdate = true
		}
	}
	mergedPostLogout := existingPostLogout
	for _, u := range wantPostLogout {
		if _, ok := postLogoutSet[u]; !ok {
			mergedPostLogout = append(mergedPostLogout, u)
			needsUpdate = true
		}
	}

	if !needsUpdate {
		fmt.Println("✅ Frontend redirect URIs are up to date")
		return nil
	}

	fmt.Printf("🔄 Updating frontend redirect URIs (adding %d redirect, %d post-logout)...\n",
		len(mergedRedirect)-len(existingRedirect), len(mergedPostLogout)-len(existingPostLogout))

	_, err := c.applicationService.UpdateApplication(c.ctx, &applicationV2.UpdateApplicationRequest{
		ApplicationId: appID,
		ProjectId:     projectID,
		ApplicationType: &applicationV2.UpdateApplicationRequest_OidcConfiguration{
			OidcConfiguration: &applicationV2.UpdateOIDCApplicationConfigurationRequest{
				RedirectUris:           mergedRedirect,
				PostLogoutRedirectUris: mergedPostLogout,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("UpdateApplication failed: %w", err)
	}

	for _, u := range mergedRedirect {
		if _, ok := redirectSet[u]; !ok {
			fmt.Printf("  ✅ Added redirect URI: %s\n", u)
		}
	}
	for _, u := range mergedPostLogout {
		if _, ok := postLogoutSet[u]; !ok {
			fmt.Printf("  ✅ Added post-logout URI: %s\n", u)
		}
	}
	return nil
}

// GetOrCreateAPIApp finds or creates the API application.
func (c *Client) GetOrCreateAPIApp(orgID, projectID string) (string, string, error) {
	// Try to find existing app
	listResp, err := c.applicationService.ListApplications(c.ctx, &applicationV2.ListApplicationsRequest{
		Filters: []*applicationV2.ApplicationSearchFilter{
			{
				Filter: &applicationV2.ApplicationSearchFilter_ProjectIdFilter{
					ProjectIdFilter: &applicationV2.ProjectIDFilter{
						ProjectId: projectID,
					},
				},
			},
			{
				Filter: &applicationV2.ApplicationSearchFilter_NameFilter{
					NameFilter: &applicationV2.ApplicationNameFilter{
						Name: "IAC Platform API",
					},
				},
			},
		},
	})
	if err == nil && listResp != nil && len(listResp.GetApplications()) > 0 {
		appID := listResp.GetApplications()[0].GetApplicationId()
		fmt.Printf("✅ Using existing API app: %s\n", appID)

		// Get the app details to retrieve the client secret
		getResp, err := c.applicationService.GetApplication(c.ctx, &applicationV2.GetApplicationRequest{
			ApplicationId: appID,
		})
		if err == nil && getResp != nil {
			if apiApp := getResp.GetApplication().GetApiConfiguration(); apiApp != nil {
				// Client secret is not returned in GetApplication, need to regenerate or get from CreateApplication response
				// For now, return empty and user will need to regenerate if needed
				return appID, "", nil
			}
		}
		// If we can't get the secret, return empty (user will need to regenerate)
		return appID, "", nil
	}

	// Create new API app
	createResp, err := c.applicationService.CreateApplication(c.ctx, &applicationV2.CreateApplicationRequest{
		ProjectId: projectID,
		Name:      "IAC Platform API",
		ApplicationType: &applicationV2.CreateApplicationRequest_ApiConfiguration{
			ApiConfiguration: &applicationV2.CreateAPIApplicationRequest{
				AuthMethodType: applicationV2.APIAuthMethodType_API_AUTH_METHOD_TYPE_BASIC,
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to create API app: %w", err)
	}

	appID := createResp.GetApplicationId()
	// Extract client secret from response if available
	var clientSecret string
	if apiResp := createResp.GetApiConfiguration(); apiResp != nil {
		clientSecret = apiResp.GetClientSecret()
	}
	fmt.Printf("✅ Created API app: %s\n", appID)
	return appID, clientSecret, nil
}
