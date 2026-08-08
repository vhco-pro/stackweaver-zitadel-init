// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"fmt"
	"time"

	applicationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/application/v2"
)

// frontendOIDCClientID reads the OIDC ClientId of an app, retrying while Zitadel's query
// projection catches up. On a cold instance GetApplication can fail (or return an OIDC config
// without ClientId) for several seconds after CreateApplication; returning the ApplicationId
// instead - the old fallback - poisons the frontend-client-id secret with a value that is not a
// valid OIDC client_id and breaks every browser login on a fresh install. Failing is safe:
// zitadel-init exits non-zero and is re-run.
func (c *Client) frontendOIDCClientID(appID string) (string, *applicationV2.OIDCConfiguration, error) {
	var lastErr error
	for attempt := range 30 {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}
		getResp, err := c.applicationService.GetApplication(c.ctx, &applicationV2.GetApplicationRequest{
			ApplicationId: appID,
		})
		if err != nil {
			lastErr = err
			continue
		}
		if app := getResp.GetApplication(); app != nil {
			if oidcConfig := app.GetOidcConfiguration(); oidcConfig != nil {
				if clientID := oidcConfig.GetClientId(); clientID != "" {
					return clientID, oidcConfig, nil
				}
			}
		}
		lastErr = fmt.Errorf("OIDC configuration has no ClientId yet")
	}
	return "", nil, fmt.Errorf("could not resolve OIDC ClientId for frontend app %s: %w", appID, lastErr)
}

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

		clientID, oidcConfig, err := c.frontendOIDCClientID(appID)
		if err != nil {
			return "", err
		}
		fmt.Printf("✅ Using existing frontend app: %s (ClientId: %s)\n", appID, clientID)

		// Ensure redirect URIs are up to date
		if err := c.ensureFrontendRedirectURIs(appID, projectID, oidcConfig, redirectURIs, postLogoutURIs); err != nil {
			fmt.Printf("⚠️  Warning: could not update redirect URIs: %v\n", err)
		}

		return clientID, nil
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

	clientID, _, err := c.frontendOIDCClientID(appID)
	if err != nil {
		return "", err
	}
	fmt.Printf("✅ Created frontend app: %s (ClientId: %s)\n", appID, clientID)

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
