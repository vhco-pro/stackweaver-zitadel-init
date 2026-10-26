// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"fmt"
	"slices"
	"strings"
	"time"

	applicationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/application/v2"
)

// Application names zitadel-init provisions in the Stackweaver project.
const (
	// frontendAppName is the SPA's public client (PKCE only). The backend-for-frontend sign-in
	// retires it: DeactivatePublicFrontendApp switches it off once the BFF client exists.
	frontendAppName = "IAC Platform Frontend"
	// bffAppName is the confidential client the API uses to complete browser sign-in.
	bffAppName = "Stackweaver BFF"
)

// frontendOIDCClientID reads the OIDC ClientId of an app, retrying while Zitadel's query
// projection catches up. On a cold instance GetApplication can fail (or return an OIDC config
// without ClientId) for several seconds after CreateApplication; returning the ApplicationId
// instead - the old fallback - stores a value that is not a valid OIDC client_id and breaks every
// browser login on a fresh install. Failing is safe:
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

// DefaultRedirectURIs returns the SPA's sign-in redirect and post-logout URIs: any extras from
// zitadel-init.yaml or the environment, and the dev Compose localhost origin when includeLocalhost
// is set. A deployment that names its own redirect (the Helm chart sets FRONTEND_REDIRECT_URI to
// the install's URL) gets no localhost entry, so its app is not put in development mode and
// accepts no localhost redirect.
func DefaultRedirectURIs(extraRedirectURIs, extraPostLogoutURIs []string, includeLocalhost bool) (redirectURIs, postLogoutURIs []string) {
	if includeLocalhost {
		redirectURIs = append(redirectURIs, "http://localhost:5173/auth/callback")
		postLogoutURIs = append(postLogoutURIs, "http://localhost:5173")
	}
	redirectURIs = append(redirectURIs, extraRedirectURIs...)
	postLogoutURIs = append(postLogoutURIs, extraPostLogoutURIs...)
	return redirectURIs, postLogoutURIs
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

// findApp returns the application named name in the project, or nil when there is none.
func (c *Client) findApp(projectID, name string) (*applicationV2.Application, error) {
	listResp, err := c.applicationService.ListApplications(c.ctx, &applicationV2.ListApplicationsRequest{
		Filters: []*applicationV2.ApplicationSearchFilter{
			{Filter: &applicationV2.ApplicationSearchFilter_ProjectIdFilter{ProjectIdFilter: &applicationV2.ProjectIDFilter{ProjectId: projectID}}},
			{Filter: &applicationV2.ApplicationSearchFilter_NameFilter{NameFilter: &applicationV2.ApplicationNameFilter{Name: name}}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("listing applications named %q: %w", name, err)
	}
	if apps := listResp.GetApplications(); len(apps) > 0 {
		return apps[0], nil
	}
	return nil, nil
}

// usesPlainHTTP reports whether any URI is plain http, which Zitadel accepts only for an app in
// development mode (dev Compose on http://localhost).
func usesPlainHTTP(uris ...[]string) bool {
	for _, list := range uris {
		for _, u := range list {
			if strings.HasPrefix(u, "http://") {
				return true
			}
		}
	}
	return false
}

// GetOrCreateBFFApp finds or creates the confidential web application the API uses to complete
// browser sign-in (the backend-for-frontend), and returns its client id and, when one is
// available, its secret.
//
// The app authenticates with a client secret (Basic), takes the authorization-code grant only
// (the BFF never requests offline_access, so it never holds a refresh token), issues JWT access
// tokens (so the sso_groups claim can be read at sign-in), asserts userinfo in the ID token (so
// the code-flow ID token carries email_verified), and names the API's back-channel logout URI.
//
// Zitadel returns a secret only when an app is created. On an existing app the secret is "" unless
// haveSecret is false, in which case a new one is generated, so an install that lost the stored
// secret recovers on the next run.
func (c *Client) GetOrCreateBFFApp(projectID string, redirectURIs, postLogoutURIs []string, backChannelLogoutURI string, haveSecret bool) (clientID, clientSecret string, err error) {
	app, err := c.findApp(projectID, bffAppName)
	if err != nil {
		return "", "", err
	}

	if app == nil {
		createResp, err := c.applicationService.CreateApplication(c.ctx, &applicationV2.CreateApplicationRequest{
			ProjectId: projectID,
			Name:      bffAppName,
			ApplicationType: &applicationV2.CreateApplicationRequest_OidcConfiguration{
				OidcConfiguration: &applicationV2.CreateOIDCApplicationRequest{
					RedirectUris:             redirectURIs,
					PostLogoutRedirectUris:   postLogoutURIs,
					ResponseTypes:            []applicationV2.OIDCResponseType{applicationV2.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE},
					GrantTypes:               []applicationV2.OIDCGrantType{applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE},
					ApplicationType:          applicationV2.OIDCApplicationType_OIDC_APP_TYPE_WEB,
					AuthMethodType:           applicationV2.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC,
					AccessTokenType:          applicationV2.OIDCTokenType_OIDC_TOKEN_TYPE_JWT,
					IdTokenUserinfoAssertion: true,
					BackChannelLogoutUri:     backChannelLogoutURI,
					DevelopmentMode:          usesPlainHTTP(redirectURIs, postLogoutURIs),
				},
			},
		})
		if err != nil {
			return "", "", fmt.Errorf("creating the BFF app: %w", err)
		}
		oidc := createResp.GetOidcConfiguration()
		clientID = oidc.GetClientId()
		if clientID == "" {
			if clientID, _, err = c.frontendOIDCClientID(createResp.GetApplicationId()); err != nil {
				return "", "", err
			}
		}
		fmt.Printf("✅ Created BFF app: %s (ClientId: %s)\n", createResp.GetApplicationId(), clientID)
		return clientID, oidc.GetClientSecret(), nil
	}

	appID := app.GetApplicationId()
	clientID, oidcConfig, err := c.frontendOIDCClientID(appID)
	if err != nil {
		return "", "", err
	}
	fmt.Printf("✅ Using existing BFF app: %s (ClientId: %s)\n", appID, clientID)

	if err := c.reconcileBFFApp(appID, projectID, oidcConfig, redirectURIs, postLogoutURIs, backChannelLogoutURI); err != nil {
		return "", "", err
	}

	if haveSecret {
		return clientID, "", nil
	}
	genResp, err := c.applicationService.GenerateClientSecret(c.ctx, &applicationV2.GenerateClientSecretRequest{
		ApplicationId: appID,
		ProjectId:     projectID,
	})
	if err != nil {
		return "", "", fmt.Errorf("generating a client secret for the BFF app: %w", err)
	}
	fmt.Println("🔑 Generated a new BFF client secret (the stored one was missing)")
	return clientID, genResp.GetClientSecret(), nil
}

// reconcileBFFApp brings an existing BFF app to the settings GetOrCreateBFFApp creates: missing
// redirect and post-logout URIs are added (existing ones are kept), and the authentication,
// grant, token and logout settings are set. It writes only when something differs.
func (c *Client) reconcileBFFApp(appID, projectID string, cfg *applicationV2.OIDCConfiguration, redirectURIs, postLogoutURIs []string, backChannelLogoutURI string) error {
	mergedRedirect := slices.Clone(cfg.GetRedirectUris())
	for _, u := range redirectURIs {
		if !slices.Contains(mergedRedirect, u) {
			mergedRedirect = append(mergedRedirect, u)
		}
	}
	mergedPostLogout := slices.Clone(cfg.GetPostLogoutRedirectUris())
	for _, u := range postLogoutURIs {
		if !slices.Contains(mergedPostLogout, u) {
			mergedPostLogout = append(mergedPostLogout, u)
		}
	}
	codeOnly := []applicationV2.OIDCGrantType{applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE}
	// With no back-channel URI configured (the Compose example), keep the one the app has, which an
	// operator may have set in the console, rather than clearing it.
	if backChannelLogoutURI == "" {
		backChannelLogoutURI = cfg.GetBackChannelLogoutUri()
	}

	upToDate := len(mergedRedirect) == len(cfg.GetRedirectUris()) &&
		len(mergedPostLogout) == len(cfg.GetPostLogoutRedirectUris()) &&
		cfg.GetIdTokenUserinfoAssertion() &&
		cfg.GetBackChannelLogoutUri() == backChannelLogoutURI &&
		cfg.GetAuthMethodType() == applicationV2.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC &&
		slices.Equal(cfg.GetGrantTypes(), codeOnly) &&
		cfg.GetAccessTokenType() == applicationV2.OIDCTokenType_OIDC_TOKEN_TYPE_JWT
	if upToDate {
		fmt.Println("✅ BFF app settings are up to date")
		return nil
	}

	basic := applicationV2.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC
	jwt := applicationV2.OIDCTokenType_OIDC_TOKEN_TYPE_JWT
	userinfo := true
	devMode := usesPlainHTTP(mergedRedirect, mergedPostLogout)
	_, err := c.applicationService.UpdateApplication(c.ctx, &applicationV2.UpdateApplicationRequest{
		ApplicationId: appID,
		ProjectId:     projectID,
		ApplicationType: &applicationV2.UpdateApplicationRequest_OidcConfiguration{
			OidcConfiguration: &applicationV2.UpdateOIDCApplicationConfigurationRequest{
				RedirectUris:             mergedRedirect,
				PostLogoutRedirectUris:   mergedPostLogout,
				GrantTypes:               codeOnly,
				AuthMethodType:           &basic,
				AccessTokenType:          &jwt,
				IdTokenUserinfoAssertion: &userinfo,
				BackChannelLogoutUri:     &backChannelLogoutURI,
				DevelopmentMode:          &devMode,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("updating the BFF app: %w", err)
	}
	fmt.Println("🔄 Updated BFF app settings")
	return nil
}

// DeactivatePublicFrontendApp switches off the SPA's public client once the BFF client exists, so
// no path on the SPA's origin can redeem an authorization code for tokens. A missing or already
// inactive app is left as it is.
func (c *Client) DeactivatePublicFrontendApp(projectID string) error {
	app, err := c.findApp(projectID, frontendAppName)
	if err != nil {
		return err
	}
	if app == nil || app.GetState() != applicationV2.ApplicationState_APPLICATION_STATE_ACTIVE {
		return nil
	}
	if _, err := c.applicationService.DeactivateApplication(c.ctx, &applicationV2.DeactivateApplicationRequest{
		ApplicationId: app.GetApplicationId(),
		ProjectId:     projectID,
	}); err != nil {
		return fmt.Errorf("deactivating the public frontend app: %w", err)
	}
	fmt.Println("🔒 Deactivated the public frontend app (sign-in now goes through the BFF client)")
	return nil
}
