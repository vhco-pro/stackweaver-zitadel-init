// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"fmt"
	"time"

	featureV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/feature/v2"
	filterpb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	internalpermission "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/internal_permission/v2"
	objectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	loginServiceUserName    = "login-ui-service"
	loginServiceDisplayName = "Login UI Service"
	loginServiceDescription = "Service user for the Login UI (auto-generated)"
)

func (c *Client) findUserIDByLoginName(loginName string) (string, error) {
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
func (c *Client) ensureAdministratorHasRole(userID, role string) error {
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

func (c *Client) ensureIAMLoginClientRole(userID string) error {
	return c.ensureAdministratorHasRole(userID, "IAM_LOGIN_CLIENT")
}

func (c *Client) createLoginServicePAT(userID string) (string, error) {
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

func (c *Client) createLoginServiceUser(orgID string) (string, error) {
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

// EnsureLoginServiceUser finds or creates the login service user, grants the
// IAM_LOGIN_CLIENT role, and creates a PAT for newly created users.
func (c *Client) EnsureLoginServiceUser() (string, string, error) {
	userID, err := c.findUserIDByLoginName(loginServiceUserName)
	if err != nil {
		return "", "", err
	}

	isNewUser := false
	if userID == "" {
		orgID := c.orgID
		if orgID == "" {
			return "", "", fmt.Errorf("organization ID is not set before creating login service user")
		}
		userID, err = c.createLoginServiceUser(orgID)
		if err != nil {
			return "", "", err
		}
		isNewUser = true
		fmt.Printf("✅ Created login service user: %s\n", userID)
	} else {
		fmt.Printf("✅ Login service user already exists: %s\n", userID)
	}

	if err := c.ensureIAMLoginClientRole(userID); err != nil {
		return "", "", err
	}

	// Only create a new PAT for newly created users. For existing users, return
	// empty string, the caller (writeKubernetesSecretFromEnv / writeConfigFiles)
	// will preserve the existing token from the K8s Secret or .env file.
	if isNewUser {
		token, err := c.createLoginServicePAT(userID)
		if err != nil {
			return "", "", err
		}
		return userID, token, nil
	}

	return userID, "", nil
}

// EnsureLoginV2BaseURI sets the Login V2 BaseURI on the Zitadel instance via the Feature API.
// This overrides whatever was stored in the database during initial setup (DefaultInstance.Features.LoginV2.BaseURI
// in defaults.yaml only applies on first init; this call updates it on every zitadel-init run).
func (c *Client) EnsureLoginV2BaseURI(baseURI string) error {
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
