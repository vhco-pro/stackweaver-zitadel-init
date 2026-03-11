// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/client"
	"gopkg.in/yaml.v3"
	actionV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/action/v2"
	applicationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/application/v2"
	featureV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/feature/v2"
	filterpb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	idppb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/idp"
	internalpermission "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/internal_permission/v2"
	instanceV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/instance/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	objectpb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object"
	objectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	orgV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	zitadelpkg "github.com/zitadel/zitadel-go/v3/pkg/zitadel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxWait                 = 300 * time.Second
	waitInterval            = 2 * time.Second
	loginServiceUserName    = "login-ui-service"
	loginServiceDisplayName = "Login UI Service"
	loginServiceDescription = "Service user for the Login UI (auto-generated)"
)

type ZitadelClient struct {
	ctx                context.Context
	api                *client.Client
	orgService         orgV2.OrganizationServiceClient
	projectService     projectV2.ProjectServiceClient
	applicationService applicationV2.ApplicationServiceClient
	userService        userV2.UserServiceClient
	internalPermission internalpermission.InternalPermissionServiceClient
	mgmtService        management.ManagementServiceClient
	actionService      actionV2.ActionServiceClient
	featureService     featureV2.FeatureServiceClient
	orgID              string
}

func NewZitadelClient(accessToken, dialAddr, domain string) (*ZitadelClient, error) {
	ctx := context.Background()

	// domain must match Zitadel's ExternalDomain so the gRPC :authority header
	// resolves to the correct instance. Docker Compose uses "localhost"; Kubernetes
	// passes the ingress auth hostname via ZITADEL_DOMAIN.
	if domain == "" {
		domain = "localhost"
	}
	zitadelInstance := zitadelpkg.New(domain, zitadelpkg.WithInsecure("8080"))

	if dialAddr == "" {
		dialAddr = "internal-zitadel:8080"
	}

	// Create a custom dialer that connects to the Docker network alias
	customDialer := func(ctx context.Context, addr string) (net.Conn, error) {
		dialer := &net.Dialer{}
		return dialer.DialContext(ctx, "tcp", dialAddr)
	}

	// Create client - SDK uses localhost for validation
	// Custom dialer connects to the provided alias
	api, err := client.New(ctx, zitadelInstance,
		client.WithAuth(client.PAT(accessToken)),
		client.WithGRPCDialOptions(
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(customDialer),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Zitadel client: %w", err)
	}

	return &ZitadelClient{
		ctx:                ctx,
		api:                api,
		orgService:         api.OrganizationServiceV2(),
		projectService:     api.ProjectServiceV2(),
		applicationService: api.ApplicationServiceV2(),
		userService:        api.UserServiceV2(),
		internalPermission: api.InternalPermissionServiceV2(),
		mgmtService:        api.ManagementService(),
		actionService:      api.ActionServiceV2(),
		featureService:     api.FeatureServiceV2(),
	}, nil
}

func (c *ZitadelClient) GetOrCreateOrg(name string) (string, error) {
	// Try to find existing org by name using v2
	listResp, err := c.orgService.ListOrganizations(c.ctx, &orgV2.ListOrganizationsRequest{
		Queries: []*orgV2.SearchQuery{
			{
				Query: &orgV2.SearchQuery_NameQuery{
					NameQuery: &orgV2.OrganizationNameQuery{
						Name: name,
					},
				},
			},
		},
	})
	if err == nil && listResp != nil && len(listResp.GetResult()) > 0 {
		orgID := listResp.GetResult()[0].GetId()
		if orgID != "" {
			c.orgID = orgID
			fmt.Printf("✅ Using existing organization: %s\n", orgID)
			return orgID, nil
		}
	}

	// Create new org using v2
	createResp, err := c.orgService.AddOrganization(c.ctx, &orgV2.AddOrganizationRequest{
		Name: name,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create org: %w", err)
	}

	orgID := createResp.GetOrganizationId()
	c.orgID = orgID
	fmt.Printf("✅ Created organization: %s\n", orgID)
	return orgID, nil
}

func (c *ZitadelClient) GetOrCreateProject(orgID, name string) (string, error) {
	// Try to find existing project
	listResp, err := c.projectService.ListProjects(c.ctx, &projectV2.ListProjectsRequest{
		Filters: []*projectV2.ProjectSearchFilter{
			{
				Filter: &projectV2.ProjectSearchFilter_ProjectNameFilter{
					ProjectNameFilter: &projectV2.ProjectNameFilter{
						ProjectName: name,
					},
				},
			},
		},
	})
	if err == nil && listResp != nil && len(listResp.GetProjects()) > 0 {
		projectID := listResp.GetProjects()[0].GetProjectId()
		fmt.Printf("✅ Using existing project: %s\n", projectID)
		return projectID, nil
	}

	// Create new project
	createResp, err := c.projectService.CreateProject(c.ctx, &projectV2.CreateProjectRequest{
		OrganizationId: orgID,
		Name:           name,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create project: %w", err)
	}

	projectID := createResp.GetProjectId()
	fmt.Printf("✅ Created project: %s\n", projectID)
	
	return projectID, nil
}

func (c *ZitadelClient) GetOrCreateFrontendApp(orgID, projectID string, extraRedirectURIs, extraPostLogoutURIs []string) (string, error) {
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
	
	// Wait for database projection to complete
	// OAuth endpoint needs the app to be fully projected before it can be found
	fmt.Printf("⏳ Waiting for app projection to complete...\n")
	time.Sleep(5 * time.Second)
	
	// Get the app to retrieve ClientId
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

// EnsureLoginV2BaseURI sets the Login V2 BaseURI on the Zitadel instance via the Feature API.
// This overrides whatever was stored in the database during initial setup (DefaultInstance.Features.LoginV2.BaseURI
// in defaults.yaml only applies on first init; this call updates it on every zitadel-init run).
func (c *ZitadelClient) EnsureLoginV2BaseURI(baseURI string) error {
	if baseURI == "" {
		return nil
	}
	_, err := c.featureService.SetInstanceFeatures(c.ctx, &featureV2.SetInstanceFeaturesRequest{
		LoginV2: &featureV2.LoginV2{
			Required: true,
			BaseUri:  &baseURI,
		},
	})
	if err != nil {
		return fmt.Errorf("SetInstanceFeatures LoginV2 BaseURI: %w", err)
	}
	fmt.Printf("✅ Login V2 BaseURI set to: %s\n", baseURI)
	return nil
}

// ensureFrontendRedirectURIs checks if the existing app's redirect URIs contain all desired URIs.
// If any are missing, it calls UpdateApplication to add them.
func (c *ZitadelClient) ensureFrontendRedirectURIs(appID, projectID string, oidcConfig *applicationV2.OIDCConfiguration, wantRedirect, wantPostLogout []string) error {
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

func (c *ZitadelClient) GetOrCreateAPIApp(orgID, projectID string) (string, string, error) {
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

func waitForPAT(patPath string) (string, error) {
	fmt.Printf("⏳ Waiting for PAT file at %s...\n", patPath)

	start := time.Now()
	for time.Since(start) < maxWait {
		data, err := os.ReadFile(patPath)
		if err == nil && len(data) > 0 {
			pat := strings.TrimSpace(string(data))
			if pat != "" {
				fmt.Println("✅ PAT file found!")
				return pat, nil
			}
		}

		if time.Since(start).Seconds() > 0 && int(time.Since(start).Seconds())%10 == 0 {
			fmt.Printf("   Waiting... (%ds)\n", int(time.Since(start).Seconds()))
		}

		time.Sleep(waitInterval)
	}

	return "", fmt.Errorf("PAT file not found after %v", maxWait)
}

func waitForZitadelReady(grpcAddr string) error {
	if grpcAddr == "" {
		grpcAddr = "internal-zitadel:8080"
	}
	fmt.Printf("⏳ Waiting for Zitadel gRPC to be ready at %s...\n", grpcAddr)

	start := time.Now()
	for time.Since(start) < maxWait {
		// Try to connect to the gRPC port
		conn, err := net.DialTimeout("tcp", grpcAddr, 2*time.Second)
		if err == nil {
			conn.Close()
			fmt.Println("✅ Zitadel gRPC is ready!")
			return nil
		}

		if time.Since(start).Seconds() > 0 && int(time.Since(start).Seconds())%10 == 0 {
			fmt.Printf("   Waiting for Zitadel... (%ds)\n", int(time.Since(start).Seconds()))
		}

		time.Sleep(waitInterval)
	}

	return fmt.Errorf("zitadel gRPC not ready after %v", maxWait)
}

func (c *ZitadelClient) findUserIDByLoginName(loginName string) (string, error) {
	resp, err := c.userService.ListUsers(c.ctx, &userV2.ListUsersRequest{
		Query: &objectV2.ListQuery{
			Limit: 1,
		},
		Queries: []*userV2.SearchQuery{
			{
				Query: &userV2.SearchQuery_LoginNameQuery{
					LoginNameQuery: &userV2.LoginNameQuery{
						LoginName: loginName,
						Method:    objectV2.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS,
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to search for login service user: %w", err)
	}
	if len(resp.GetResult()) == 0 {
		return "", nil
	}
	return resp.GetResult()[0].GetUserId(), nil
}

// ensureAdministratorHasRole adds the given role to the user on the instance if not already present.
func (c *ZitadelClient) ensureAdministratorHasRole(userID, role string) error {
	resourceFilter := &internalpermission.AdministratorSearchFilter{
		Filter: &internalpermission.AdministratorSearchFilter_Resource{
			Resource: &internalpermission.ResourceFilter{
				Resource: &internalpermission.ResourceFilter_Instance{Instance: true},
			},
		},
	}
	userFilter := &internalpermission.AdministratorSearchFilter{
		Filter: &internalpermission.AdministratorSearchFilter_InUserIdsFilter{
			InUserIdsFilter: &filterpb.InIDsFilter{Ids: []string{userID}},
		},
	}

	listReq := &internalpermission.ListAdministratorsRequest{
		Pagination: &filterpb.PaginationRequest{Limit: 1},
		Filters:    []*internalpermission.AdministratorSearchFilter{userFilter, resourceFilter},
	}

	resp, err := c.internalPermission.ListAdministrators(c.ctx, listReq)
	if err != nil {
		return fmt.Errorf("failed to list administrators: %w", err)
	}

	instanceResource := &internalpermission.ResourceType{
		Resource: &internalpermission.ResourceType_Instance{Instance: true},
	}

	if len(resp.GetAdministrators()) == 0 {
		_, err := c.internalPermission.CreateAdministrator(c.ctx, &internalpermission.CreateAdministratorRequest{
			UserId:   userID,
			Resource: instanceResource,
			Roles:    []string{role},
		})
		if err != nil {
			return fmt.Errorf("failed to grant %s role: %w", role, err)
		}
		fmt.Printf("✅ Granted %s role\n", role)
		return nil
	}

	current := resp.GetAdministrators()[0].GetRoles()
	if containsString(current, role) {
		return nil
	}

	updated := uniqueStrings(append(current, role))
	_, err = c.internalPermission.UpdateAdministrator(c.ctx, &internalpermission.UpdateAdministratorRequest{
		UserId:   userID,
		Resource: instanceResource,
		Roles:    updated,
	})
	if err != nil {
		return fmt.Errorf("failed to update administrator roles: %w", err)
	}
	fmt.Printf("✅ Granted %s role\n", role)
	return nil
}

func (c *ZitadelClient) ensureIAMLoginClientRole(userID string) error {
	return c.ensureAdministratorHasRole(userID, "IAM_LOGIN_CLIENT")
}

func (c *ZitadelClient) createLoginServicePAT(userID string) (string, error) {
	expiration := timestamppb.New(time.Now().Add(5 * 365 * 24 * time.Hour))
	resp, err := c.userService.AddPersonalAccessToken(c.ctx, &userV2.AddPersonalAccessTokenRequest{
		UserId:         userID,
		ExpirationDate: expiration,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create login service PAT: %w", err)
	}

	token := resp.GetToken()
	if token == "" {
		return "", fmt.Errorf("login service PAT is empty")
	}
	return token, nil
}

func stringPtr(val string) *string {
	return &val
}

func (c *ZitadelClient) createLoginServiceUser(orgID string) (string, error) {
	req := &userV2.CreateUserRequest{
		OrganizationId: orgID,
		Username:       stringPtr(loginServiceUserName),
		UserType: &userV2.CreateUserRequest_Machine_{
			Machine: &userV2.CreateUserRequest_Machine{
				Name:        loginServiceDisplayName,
				Description: stringPtr(loginServiceDescription),
			},
		},
	}

	resp, err := c.userService.CreateUser(c.ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to create login service user: %w", err)
	}
	return resp.GetId(), nil
}

func (c *ZitadelClient) EnsureLoginServiceUser() (string, string, error) {
	userID, err := c.findUserIDByLoginName(loginServiceUserName)
	if err != nil {
		return "", "", err
	}

	if userID == "" {
		orgID := c.orgID
		if orgID == "" {
			return "", "", fmt.Errorf("organization ID is not set before creating login service user")
		}
		userID, err = c.createLoginServiceUser(orgID)
		if err != nil {
			return "", "", err
		}
		fmt.Printf("✅ Created login service user: %s\n", userID)
	} else {
		fmt.Printf("✅ Login service user already exists: %s\n", userID)
	}

	if err := c.ensureIAMLoginClientRole(userID); err != nil {
		return "", "", err
	}

	token, err := c.createLoginServicePAT(userID)
	if err != nil {
		return "", "", err
	}

	return userID, token, nil
}

// findProviderByName searches for an existing IdP provider by name.
// Returns the provider ID if found, empty string if not found.
func (c *ZitadelClient) findProviderByName(name string) (string, error) {
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
func (c *ZitadelClient) ensureCustomLoginPolicy() error {
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
func (c *ZitadelClient) addIDPToLoginPolicy(idpID string) error {
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
func (c *ZitadelClient) ConfigureAzureADProvider(clientID, clientSecret, tenantID string) error {
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
			IsAutoCreation:   true,                                            // JIT provisioning
			IsAutoUpdate:     true,                                            // Sync profile on subsequent logins
			IsLinkingAllowed: true,                                            // Link to existing Zitadel users
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
func (c *ZitadelClient) ConfigureGenericOIDCProvider(name, issuer, clientID, clientSecret string) error {
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

// loadZitadelDefaults reads ExternalDomain, ExternalPort, ExternalSecure from deploy/zitadel-defaults.yaml.
// Falls back to localhost:8080 insecure if the file is missing or incomplete.
func loadZitadelDefaults(projectRoot string) ZitadelDefaultsConfig {
	defaults := ZitadelDefaultsConfig{
		ExternalDomain: "localhost",
		ExternalPort:   8080,
		ExternalSecure: false,
	}
	path := filepath.Join(projectRoot, "deploy", "zitadel-defaults.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("⚠️  Could not read %s, using defaults: %v\n", path, err)
		return defaults
	}
	var cfg ZitadelDefaultsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Printf("⚠️  Could not parse %s, using defaults: %v\n", path, err)
		return defaults
	}
	if cfg.ExternalDomain != "" {
		defaults.ExternalDomain = cfg.ExternalDomain
	}
	if cfg.ExternalPort != 0 {
		defaults.ExternalPort = cfg.ExternalPort
	}
	defaults.ExternalSecure = cfg.ExternalSecure
	return defaults
}

// computeIssuerURL derives the Zitadel issuer URL from ExternalDomain/Port/Secure.
// Examples:
//   - localhost:8080 insecure  → http://localhost:8080
//   - zitadel.example.com:443 secure → https://zitadel.example.com
func computeIssuerURL(cfg ZitadelDefaultsConfig) string {
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

// loadZitadelInitConfig reads deploy/zitadel-init.yaml under projectRoot.
// Returns nil if the file is missing or invalid (no error; config is optional).
func loadZitadelInitConfig(projectRoot string) *ZitadelInitConfig {
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

// loadCustomDomainsConfig returns custom domains from env ZITADEL_CUSTOM_DOMAINS (comma-separated)
// or from deploy/zitadel-init.yaml (custom_domains list). Env takes precedence.
func loadCustomDomainsConfig(projectRoot string) []string {
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
	cfg := loadZitadelInitConfig(projectRoot)
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

// EnsureTrustedDomains adds each desired domain as a trusted domain on the instance if not already present.
// This allows Zitadel to accept OIDC requests from both ExternalDomain (e.g. localhost) and the trusted domain(s).
// Uses the Trusted Domains API which requires only iam.write (available to IAM_OWNER),
// unlike Custom Domains which requires system.domain.write (system-level auth).
// Idempotent: skips domains that are already registered.
func (c *ZitadelClient) EnsureTrustedDomains(domains []string) error {
	if len(domains) == 0 {
		return nil
	}

	instanceSvc := c.api.InstanceServiceV2()

	// List existing trusted domains (no InstanceId → uses context/host, needs only iam.read)
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
		// No InstanceId → uses context/host, needs only iam.write
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

// ConfigureIdentityProviders configures external identity providers based on environment variables.
// This is conditional: only configures providers whose env vars are set.
func (c *ZitadelClient) ConfigureIdentityProviders() error {
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

// findTargetByName searches for an existing Actions V2 target by name.
// Returns the target ID if found, empty string if not found.
func (c *ZitadelClient) findTargetByName(name string) (string, error) {
	resp, err := c.actionService.ListTargets(c.ctx, &actionV2.ListTargetsRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to list targets: %w", err)
	}
	for _, t := range resp.GetTargets() {
		if t.GetName() == name {
			return t.GetId(), nil
		}
	}
	return "", nil
}

// getOrCreateTarget creates an Actions V2 target or updates it if it already exists.
// Returns the target ID and the signing key (only available on create/update).
func (c *ZitadelClient) getOrCreateTarget(name, endpoint string) (string, string, error) {
	existingID, err := c.findTargetByName(name)
	if err != nil {
		return "", "", err
	}

	timeout := durationpb.New(10 * time.Second)

	if existingID != "" {
		// Target already exists — do NOT update it to avoid rotating the signing key.
		// Key rotation causes a race condition: docker compose reads .env before
		// zitadel-init runs, so the API container gets stale keys.
		// The endpoint URL is stable, so there's no need to update.
		fmt.Printf("✅ Using existing target '%s': %s\n", name, existingID)
		// Return empty signing key — caller will preserve existing key from .env
		return existingID, "", nil
	}

	// Create new target
	resp, err := c.actionService.CreateTarget(c.ctx, &actionV2.CreateTargetRequest{
		Name: name,
		TargetType: &actionV2.CreateTargetRequest_RestCall{
			RestCall: &actionV2.RESTCall{
				InterruptOnError: false,
			},
		},
		Timeout:  timeout,
		Endpoint: endpoint,
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to create target '%s': %w", name, err)
	}

	targetID := resp.GetId()
	signingKey := resp.GetSigningKey()
	fmt.Printf("✅ Created target '%s': %s\n", name, targetID)
	return targetID, signingKey, nil
}

// setExecution creates or updates an Actions V2 execution that binds a target to a condition.
func (c *ZitadelClient) setExecution(condition *actionV2.Condition, targetID string) error {
	_, err := c.actionService.SetExecution(c.ctx, &actionV2.SetExecutionRequest{
		Condition: condition,
		Targets:   []string{targetID},
	})
	if err != nil {
		return fmt.Errorf("failed to set execution: %w", err)
	}
	return nil
}

// cleanupActionsV1 removes any legacy Actions V1 actions and triggers.
// This is needed when migrating from V1 to V2 — the old triggers won't fire
// with Login V2, so they should be cleaned up.
func (c *ZitadelClient) cleanupActionsV1() {
	// Clear V1 triggers first (empty action lists)
	// FlowType "1" = External Authentication, TriggerType "1" = Post Authentication
	_, _ = c.mgmtService.SetTriggerActions(c.ctx, &management.SetTriggerActionsRequest{
		FlowType:    "1",
		TriggerType: "1",
		ActionIds:   []string{},
	})
	// FlowType "2" = Complement Token, TriggerType "5" = Pre Access Token Creation
	_, _ = c.mgmtService.SetTriggerActions(c.ctx, &management.SetTriggerActionsRequest{
		FlowType:    "2",
		TriggerType: "5",
		ActionIds:   []string{},
	})

	// Delete V1 actions if they exist
	for _, name := range []string{"captureGroups", "complementTokenGroups", "sw-capture-sso-groups", "sw-complement-token-groups"} {
		id, _ := c.findActionByName(name)
		if id != "" {
			_, _ = c.mgmtService.DeleteAction(c.ctx, &management.DeleteActionRequest{Id: id})
			fmt.Printf("🗑️  Removed legacy Actions V1 action '%s': %s\n", name, id)
		}
	}
}

// findActionByName searches for an existing V1 action by name (used for cleanup).
func (c *ZitadelClient) findActionByName(name string) (string, error) {
	resp, err := c.mgmtService.ListActions(c.ctx, &management.ListActionsRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to list actions: %w", err)
	}
	for _, a := range resp.GetResult() {
		if a.GetName() == name {
			return a.GetId(), nil
		}
	}
	return "", nil
}

// ConfigureActions sets up Zitadel Actions V2 for SSO group claim passthrough.
//
// Actions V2 uses external HTTP webhooks instead of embedded JavaScript.
// This is the officially supported mechanism for Login V2, which uses the Session API
// and does NOT trigger Actions V1 flows. See: https://github.com/zitadel/zitadel/issues/11095
//
// Two webhooks are configured:
//
// 1. IDP Sync (Response on RetrieveIdentityProviderIntent):
//    When a user authenticates via an external IdP, Zitadel sends the IdP's raw claims
//    (including group memberships) to our webhook. The webhook extracts the groups
//    and stores them as user metadata in Zitadel. This is provider-agnostic and works
//    with any OIDC provider (Azure AD, Okta, Cognito, Google, etc.).
//
// 2. Complement Token (Function preaccesstoken):
//    Before every access token is created, Zitadel sends the user's metadata to our webhook.
//    The webhook reads the sso_groups metadata and includes it as a custom claim in the JWT,
//    which StackWeaver then uses for automatic team assignment.
//
// Returns the signing keys for the created targets so they can be passed to the API server.
func (c *ZitadelClient) ConfigureActions() (idpSyncKey, complementTokenKey string, err error) {
	azureClientID := os.Getenv("AZURE_AD_CLIENT_ID")
	oidcClientID := os.Getenv("OIDC_IDP_CLIENT_ID")

	if azureClientID == "" && oidcClientID == "" {
		// No external IdPs configured, skip actions
		return "", "", nil
	}

	fmt.Println()
	fmt.Println("--- Configuring Zitadel Actions V2 for SSO Group Claims ---")

	// Clean up any legacy Actions V1 configuration
	c.cleanupActionsV1()

	// The API server webhook endpoint. Since all services use network_mode: host,
	// Zitadel can reach the API at localhost:8022.
	apiBaseURL := "http://localhost:8022"

	// Target 1: IDP Sync webhook
	// Receives the RetrieveIdentityProviderIntent response with IdP claims.
	// Extracts groups and stores them as user metadata.
	idpSyncEndpoint := apiBaseURL + "/api/v2/zitadel/actions/idp-sync"
	idpSyncTargetID, idpSyncSigningKey, err := c.getOrCreateTarget("stackweaver-idp-sync", idpSyncEndpoint)
	if err != nil {
		return "", "", fmt.Errorf("failed to create IDP sync target: %w", err)
	}

	// Target 2: Complement Token webhook
	// Reads user metadata and appends sso_groups claim to access tokens.
	complementTokenEndpoint := apiBaseURL + "/api/v2/zitadel/actions/complement-token"
	complementTokenTargetID, complementTokenSigningKey, err := c.getOrCreateTarget("stackweaver-complement-token", complementTokenEndpoint)
	if err != nil {
		return "", "", fmt.Errorf("failed to create complement token target: %w", err)
	}

	// Execution 1: Trigger IDP Sync after external authentication
	// Condition: Response on /zitadel.user.v2.UserService/RetrieveIdentityProviderIntent
	err = c.setExecution(
		&actionV2.Condition{
			ConditionType: &actionV2.Condition_Response{
				Response: &actionV2.ResponseExecution{
					Condition: &actionV2.ResponseExecution_Method{
						Method: "/zitadel.user.v2.UserService/RetrieveIdentityProviderIntent",
					},
				},
			},
		},
		idpSyncTargetID,
	)
	if err != nil {
		return "", "", fmt.Errorf("failed to set IDP sync execution: %w", err)
	}
	fmt.Println("✅ Set execution: Response on RetrieveIdentityProviderIntent → IDP Sync webhook")

	// Execution 2: Add sso_groups claim to access tokens
	// Condition: Function preaccesstoken
	err = c.setExecution(
		&actionV2.Condition{
			ConditionType: &actionV2.Condition_Function{
				Function: &actionV2.FunctionExecution{
					Name: "preaccesstoken",
				},
			},
		},
		complementTokenTargetID,
	)
	if err != nil {
		return "", "", fmt.Errorf("failed to set complement token execution: %w", err)
	}
	fmt.Println("✅ Set execution: Function preaccesstoken → Complement Token webhook")

	return idpSyncSigningKey, complementTokenSigningKey, nil
}

func containsString(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		result = append(result, v)
	}
	return result
}

// ── Kubernetes helpers ────────────────────────────────────────────────────────

const (
	k8sTokenPath     = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	k8sCAPath        = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	k8sNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

func isRunningInKubernetes() bool {
	_, err := os.Stat(k8sTokenPath)
	return err == nil
}

// k8sClient holds the shared HTTP client and bearer token for in-cluster API
// calls. Create once with newK8sClient() and reuse for all operations.
type k8sClient struct {
	http    *http.Client
	token   string
	apiBase string
}

func newK8sClient() (*k8sClient, error) {
	ca, err := os.ReadFile(k8sCAPath)
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)

	token, err := os.ReadFile(k8sTokenPath)
	if err != nil {
		return nil, fmt.Errorf("read service account token: %w", err)
	}

	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	return &k8sClient{
		http: &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
			Timeout:   15 * time.Second,
		},
		token:   strings.TrimSpace(string(token)),
		apiBase: fmt.Sprintf("https://%s:%s", host, port),
	}, nil
}

// patchSecret patches the named Secret with stringData.
// The secret must already exist (created by the Helm chart).
func (k *k8sClient) patchSecret(secretName, namespace string, data map[string]string) error {
	body, err := json.Marshal(map[string]any{"stringData": data})
	if err != nil {
		return fmt.Errorf("marshal secret patch: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/namespaces/%s/secrets/%s", k.apiBase, namespace, secretName)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/merge-patch+json")

	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("patch secret: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("patch secret returned HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// restartDeployment triggers a rolling restart by patching the restartedAt
// annotation on the pod template. If Stakater Reloader annotations are present
// on the deployment, the explicit restart is skipped — Reloader will handle it
// automatically when the Secret changes.
func (k *k8sClient) restartDeployment(deploymentName, namespace, zitadelSecretName string) error {
	url := fmt.Sprintf("%s/apis/apps/v1/namespaces/%s/deployments/%s", k.apiBase, namespace, deploymentName)

	// GET the deployment to inspect Reloader annotations.
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build GET request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+k.token)

	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		fmt.Printf("  ℹ️  deployment %q not found — skipping restart\n", deploymentName)
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("get deployment returned HTTP %d: %s", resp.StatusCode, string(b))
	}

	var deploy struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&deploy); err != nil {
		return fmt.Errorf("decode deployment: %w", err)
	}

	ann := deploy.Metadata.Annotations
	if ann["reloader.stakater.com/auto"] == "true" {
		fmt.Printf("  ℹ️  Reloader auto-watch detected on %q — skipping manual restart\n", deploymentName)
		return nil
	}
	if reload := ann["secret.reloader.stakater.com/reload"]; reload != "" {
		for _, name := range strings.Split(reload, ",") {
			if strings.TrimSpace(name) == zitadelSecretName {
				fmt.Printf("  ℹ️  Reloader secret-watch detected on %q — skipping manual restart\n", deploymentName)
				return nil
			}
		}
	}

	// No Reloader — patch the restartedAt annotation.
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]string{
						"kubectl.kubernetes.io/restartedAt": time.Now().UTC().Format(time.RFC3339),
					},
				},
			},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal restart patch: %w", err)
	}

	req2, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build PATCH request: %w", err)
	}
	req2.Header.Set("Authorization", "Bearer "+k.token)
	req2.Header.Set("Content-Type", "application/merge-patch+json")

	resp2, err := k.http.Do(req2)
	if err != nil {
		return fmt.Errorf("patch deployment: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
		b, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("patch deployment returned HTTP %d: %s", resp2.StatusCode, string(b))
	}
	return nil
}

// writeKubernetesSecretFromEnv reads K8S_* env vars and patches the Zitadel
// Secret with the generated credentials.
func writeKubernetesSecretFromEnv(k *k8sClient, frontendClientID, apiClientID, apiClientSecret, loginServiceToken, idpSyncKey, complementTokenKey string) error {
	secretName := os.Getenv("K8S_SECRET_NAME")
	namespace := os.Getenv("K8S_NAMESPACE")
	if namespace == "" {
		// Fallback: read from the service account namespace file.
		if b, err := os.ReadFile(k8sNamespacePath); err == nil {
			namespace = strings.TrimSpace(string(b))
		}
	}
	if secretName == "" || namespace == "" {
		return fmt.Errorf("K8S_SECRET_NAME and K8S_NAMESPACE must be set")
	}

	keyClientID := envOrDefault("K8S_KEY_CLIENT_ID", "client-id")
	keyClientSecret := envOrDefault("K8S_KEY_CLIENT_SECRET", "client-secret")
	keyLoginToken := envOrDefault("K8S_KEY_LOGIN_SERVICE_USER_TOKEN", "login-service-user-token")
	keyFrontendClientID := envOrDefault("K8S_KEY_FRONTEND_CLIENT_ID", "frontend-client-id")
	keyIdpSyncKey := envOrDefault("K8S_KEY_WEBHOOK_IDP_SYNC_KEY", "webhook-idp-sync-key")
	keyComplementTokenKey := envOrDefault("K8S_KEY_WEBHOOK_COMPLEMENT_TOKEN_KEY", "webhook-complement-token-key")

	data := map[string]string{
		keyClientID:           apiClientID,
		keyClientSecret:       apiClientSecret,
		keyLoginToken:         loginServiceToken,
		keyFrontendClientID:   frontendClientID,
		keyIdpSyncKey:         idpSyncKey,
		keyComplementTokenKey: complementTokenKey,
	}

	if err := k.patchSecret(secretName, namespace, data); err != nil {
		return fmt.Errorf("patch zitadel secret %q: %w", secretName, err)
	}
	fmt.Printf("✅ Patched Kubernetes secret %q with Zitadel credentials\n", secretName)
	return nil
}

// restartZitadelConsumers restarts the API, frontend, and login-ui deployments
// (skipping any that aren't found or that Reloader already watches).
func restartZitadelConsumers(k *k8sClient) {
	secretName := os.Getenv("K8S_SECRET_NAME")
	namespace := os.Getenv("K8S_NAMESPACE")

	for _, envVar := range []string{"K8S_API_DEPLOYMENT", "K8S_FRONTEND_DEPLOYMENT", "K8S_LOGIN_UI_DEPLOYMENT"} {
		name := os.Getenv(envVar)
		if name == "" {
			continue
		}
		fmt.Printf("  → restarting %q ...\n", name)
		if err := k.restartDeployment(name, namespace, secretName); err != nil {
			fmt.Printf("  ⚠️  restart %q: %v\n", name, err)
		}
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Config file writer ────────────────────────────────────────────────────────

func writeConfigFiles(projectRoot, frontendClientID, apiClientID, apiClientSecret, loginServiceToken, loginServiceUserID, idpSyncKey, complementTokenKey string) error {
	// Write deploy/.env - SINGLE SOURCE OF TRUTH for all environment variables
	// All docker-compose services read from this file via env_file: - ./.env
	deployEnvPath := filepath.Join(projectRoot, "deploy", ".env")
	if err := os.MkdirAll(filepath.Dir(deployEnvPath), 0755); err != nil {
		return fmt.Errorf("failed to create deploy dir: %w", err)
	}

	// If signing keys are empty (target already existed, no rotation),
	// preserve existing keys from the current .env file.
	if idpSyncKey == "" || complementTokenKey == "" {
		existingEnv, _ := os.ReadFile(deployEnvPath)
		if existingEnv != nil {
			for _, line := range strings.Split(string(existingEnv), "\n") {
				if idpSyncKey == "" && strings.HasPrefix(line, "ZITADEL_WEBHOOK_IDP_SYNC_KEY=") {
					idpSyncKey = strings.TrimPrefix(line, "ZITADEL_WEBHOOK_IDP_SYNC_KEY=")
				}
				if complementTokenKey == "" && strings.HasPrefix(line, "ZITADEL_WEBHOOK_COMPLEMENT_TOKEN_KEY=") {
					complementTokenKey = strings.TrimPrefix(line, "ZITADEL_WEBHOOK_COMPLEMENT_TOKEN_KEY=")
				}
			}
		}
	}

	// Derive issuer URL from zitadel-defaults.yaml (ExternalDomain/Port/Secure)
	zitadelDefaults := loadZitadelDefaults(projectRoot)
	issuerURL := computeIssuerURL(zitadelDefaults)
	fmt.Printf("ℹ️  Derived issuer URL from zitadel-defaults.yaml: %s\n", issuerURL)

	// Derive external host for login-ui IdP callback header.
	// When ExternalDomain is a custom domain (not localhost), the login-ui must
	// send x-zitadel-instance-host so Zitadel constructs IdP callback URLs
	// using the external domain (e.g. https://zitadel.example.com/idps/callback)
	// instead of https://localhost:8080/idps/callback.
	externalHost := ""
	if zitadelDefaults.ExternalDomain != "localhost" {
		externalHost = zitadelDefaults.ExternalDomain
	}

	deployEnv := fmt.Sprintf(`ZITADEL_FRONTEND_CLIENT_ID=%s
ZITADEL_API_CLIENT_ID=%s
ZITADEL_API_CLIENT_SECRET=%s
ZITADEL_LOGIN_SERVICE_USER_TOKEN=%s
ZITADEL_LOGIN_SERVICE_USER_ID=%s
ZITADEL_ISSUER=%s
VITE_ZITADEL_ISSUER=%s
ZITADEL_EXTERNAL_HOST=%s
ZITADEL_WEBHOOK_IDP_SYNC_KEY=%s
ZITADEL_WEBHOOK_COMPLEMENT_TOKEN_KEY=%s
`, frontendClientID, apiClientID, apiClientSecret, loginServiceToken, loginServiceUserID, issuerURL, issuerURL, externalHost, idpSyncKey, complementTokenKey)

	if err := os.WriteFile(deployEnvPath, []byte(deployEnv), 0644); err != nil {
		return fmt.Errorf("failed to write deploy/.env: %w", err)
	}
	fmt.Println("✅ Wrote deploy/.env (single source of truth for all env vars)")

	// NOTE: Only deploy/.env is written - this is the single source of truth
	// All docker-compose services use env_file: - ./.env which reads from deploy/.env
	// docker-compose.yml uses ${ZITADEL_ISSUER:-http://localhost:8080} substitution
	// so the issuer URL is automatically propagated to API, frontend, and login-ui.
	// On first run (no .env yet), the fallback http://localhost:8080 is used.
	// After init completes, restart services to pick up the correct issuer.

	return nil
}

func main() {
	patPath := os.Getenv("ZITADEL_PAT_PATH")
	if patPath == "" {
		patPath = "/pat/admin.pat"
	}

	projectRoot := os.Getenv("PROJECT_ROOT")
	if projectRoot == "" {
		projectRoot = "/config"
	}

	fmt.Println("==========================================")
	fmt.Println("Zitadel Initialization (v2 gRPC)")
	fmt.Println("==========================================")
	fmt.Println()

	internalAddr := os.Getenv("ZITADEL_INTERNAL_ADDR")
	if internalAddr == "" {
		internalAddr = "internal-zitadel:8080"
	}

	// ZITADEL_DOMAIN must match Zitadel's ExternalDomain so the gRPC :authority
	// header resolves to the correct instance. Defaults to "localhost" for Docker Compose.
	domain := os.Getenv("ZITADEL_DOMAIN")

	var accessToken string
	if patFromEnv := os.Getenv("ZITADEL_PAT"); patFromEnv != "" {
		accessToken = patFromEnv
		fmt.Println("✅ Using PAT from environment variable")
	} else {
		pat, err := waitForPAT(patPath)
		if err != nil {
			fmt.Printf("❌ PAT not found: %v\n", err)
			fmt.Println()
			fmt.Println("Please provide ZITADEL_PAT environment variable or ensure PAT file exists at:", patPath)
			os.Exit(1)
		}
		accessToken = pat
		fmt.Println("✅ Using PAT from file")
	}

	// Wait for Zitadel gRPC to be ready before connecting (via Docker alias)
	if err := waitForZitadelReady(internalAddr); err != nil {
		fmt.Printf("❌ Zitadel not ready: %v\n", err)
		os.Exit(1)
	}

	// Create client - domain sets the gRPC :authority header to match Zitadel's ExternalDomain
	client, err := NewZitadelClient(accessToken, internalAddr, domain)
	if err != nil {
		fmt.Printf("❌ Failed to create Zitadel client: %v\n", err)
		os.Exit(1)
	}

	// Create or get organization
	orgID, err := client.GetOrCreateOrg("IAC Platform")
	if err != nil {
		fmt.Printf("❌ Failed to get/create org: %v\n", err)
		os.Exit(1)
	}

	// Create or get project
	projectID, err := client.GetOrCreateProject(orgID, "IAC Platform Project")
	if err != nil {
		fmt.Printf("❌ Failed to get/create project: %v\n", err)
		os.Exit(1)
	}

	// Load extra redirect URIs from zitadel-init.yaml config
	initCfg := loadZitadelInitConfig(projectRoot)
	var extraRedirectURIs, extraPostLogoutURIs []string
	if initCfg != nil {
		extraRedirectURIs = initCfg.FrontendRedirectURIs
		extraPostLogoutURIs = initCfg.FrontendPostLogoutRedirectURIs
	}

	// Also read redirect URIs from environment variables (used by the Helm chart
	// to inject the production app URL without requiring a mounted config file).
	if v := os.Getenv("FRONTEND_REDIRECT_URI"); v != "" {
		extraRedirectURIs = append(extraRedirectURIs, v)
	}
	if v := os.Getenv("FRONTEND_POST_LOGOUT_URI"); v != "" {
		extraPostLogoutURIs = append(extraPostLogoutURIs, v)
	}

	// Create or get frontend app (with extra redirect URIs for tunnel access)
	frontendClientID, err := client.GetOrCreateFrontendApp(orgID, projectID, extraRedirectURIs, extraPostLogoutURIs)
	if err != nil {
		fmt.Printf("❌ Failed to get/create frontend app: %v\n", err)
		os.Exit(1)
	}

	// Create or get API app
	apiClientID, apiClientSecret, err := client.GetOrCreateAPIApp(orgID, projectID)
	if err != nil {
		fmt.Printf("❌ Failed to get/create API app: %v\n", err)
		os.Exit(1)
	}

	// Ensure login service user and PAT for the Login v2 UI
	loginUserID, loginServiceToken, err := client.EnsureLoginServiceUser()
	if err != nil {
		fmt.Printf("❌ Failed to prepare login service user: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Login service user ready: %s\n", loginUserID)

	// Ensure the Login V2 BaseURI points to the public login-ui URL.
	// The ConfigMap's DefaultInstance.Features.LoginV2.BaseURI only applies on first Zitadel init
	// and uses the internal service name (not browser-reachable). This call updates the database
	// via the Feature API on every zitadel-init run, so the URL stays correct after domain changes.
	if loginUIBaseURL := os.Getenv("LOGIN_UI_BASE_URL"); loginUIBaseURL != "" {
		if err := client.EnsureLoginV2BaseURI(loginUIBaseURL); err != nil {
			fmt.Printf("⚠️  Warning: could not set Login V2 BaseURI: %v\n", err)
		}
	}

	// Ensure trusted + custom domains so Zitadel is reachable on both ExternalDomain and localhost.
	// Sources: env ZITADEL_CUSTOM_DOMAINS (comma-separated) or deploy/zitadel-init.yaml (custom_domains).
	// When ExternalDomain != localhost, "localhost" is automatically added so internal services
	// can still reach Zitadel without going through the external domain.
	domains := loadCustomDomainsConfig(projectRoot)

	// Auto-add localhost when ExternalDomain is a real domain
	zitadelDefaults := loadZitadelDefaults(projectRoot)
	if zitadelDefaults.ExternalDomain != "localhost" && zitadelDefaults.ExternalDomain != "" {
		// Ensure localhost is in the list so internal services work
		hasLocalhost := false
		for _, d := range domains {
			if d == "localhost" {
				hasLocalhost = true
				break
			}
		}
		if !hasLocalhost {
			domains = append(domains, "localhost")
			fmt.Printf("ℹ️  ExternalDomain is %q — auto-adding localhost as custom domain for internal access\n", zitadelDefaults.ExternalDomain)
		}
	}

	if len(domains) > 0 {
		fmt.Println()
		fmt.Println("--- Ensuring trusted domains ---")
		if err := client.EnsureTrustedDomains(domains); err != nil {
			fmt.Printf("❌ Failed to ensure trusted domains: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("ℹ️  No custom domains configured (set custom_domains in deploy/zitadel-init.yaml or ZITADEL_CUSTOM_DOMAINS)")
	}

	// Configure external identity providers (Azure AD, Generic OIDC)
	// Only configures if the relevant environment variables are set
	if err := client.ConfigureIdentityProviders(); err != nil {
		fmt.Printf("❌ Failed to configure identity providers: %v\n", err)
		os.Exit(1)
	}

	// Configure Zitadel Actions V2 for SSO group claim passthrough
	// Uses external webhooks instead of embedded JavaScript (V1 doesn't work with Login V2)
	// Only configures if at least one external IdP is set up
	idpSyncKey, complementTokenKey, err := client.ConfigureActions()
	if err != nil {
		fmt.Printf("❌ Failed to configure Zitadel Actions: %v\n", err)
		os.Exit(1)
	}

	// Write credentials — K8s: patch the Secret directly; Docker Compose: write .env.
	if isRunningInKubernetes() {
		k8s, err := newK8sClient()
		if err != nil {
			fmt.Printf("❌ Failed to create Kubernetes client: %v\n", err)
			os.Exit(1)
		}
		if err := writeKubernetesSecretFromEnv(k8s, frontendClientID, apiClientID, apiClientSecret, loginServiceToken, idpSyncKey, complementTokenKey); err != nil {
			fmt.Printf("❌ Failed to patch Kubernetes secret: %v\n", err)
			os.Exit(1)
		}
		fmt.Println()
		fmt.Println("--- Restarting Zitadel consumers ---")
		restartZitadelConsumers(k8s)
	} else {
		if err := writeConfigFiles(projectRoot, frontendClientID, apiClientID, apiClientSecret, loginServiceToken, loginUserID, idpSyncKey, complementTokenKey); err != nil {
			fmt.Printf("❌ Failed to write config files: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Println()
	fmt.Println("==========================================")
	fmt.Println("✅ Zitadel initialization complete!")
	fmt.Println("==========================================")
	fmt.Println()
	fmt.Printf("Frontend Client ID: %s\n", frontendClientID)
	fmt.Printf("API Client ID: %s\n", apiClientID)
	fmt.Println()

	// In Kubernetes the sidecar must not exit (Deployment restartPolicy: Always
	// would cause a restart loop). Serve a health endpoint and block forever;
	// if the pod restarts due to an upgrade, the sidecar re-provisions idempotently.
	if isRunningInKubernetes() {
		healthPort := os.Getenv("HEALTH_PORT")
		if healthPort == "" {
			healthPort = "8081"
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ok")
		})
		go func() {
			fmt.Printf("Health endpoint listening on :%s/healthz\n", healthPort)
			if err := http.ListenAndServe(":"+healthPort, mux); err != nil {
				fmt.Printf("⚠️  Health server error: %v\n", err)
			}
		}()
		fmt.Println("Provisioning complete, entering idle state (sidecar mode).")
		select {}
	}

	fmt.Println("Configuration files have been written.")
	fmt.Println("Restart your services to pick up the new config.")
}
