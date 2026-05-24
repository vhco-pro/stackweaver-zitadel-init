// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"fmt"
	"os"
	"time"

	actionV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/action/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	"google.golang.org/protobuf/types/known/durationpb"
)

// findTargetByName searches for an existing Actions V2 target by name.
// Returns the target ID if found, empty string if not found.
func (c *Client) findTargetByName(name string) (string, error) {
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
func (c *Client) getOrCreateTarget(name, endpoint string) (string, string, error) {
	existingID, err := c.findTargetByName(name)
	if err != nil {
		return "", "", err
	}

	timeout := durationpb.New(10 * time.Second)

	if existingID != "" {
		// Target already exists, do NOT update it to avoid rotating the signing key.
		// Key rotation causes a race condition: docker compose reads .env before
		// zitadel-init runs, so the API container gets stale keys.
		// The endpoint URL is stable, so there's no need to update.
		fmt.Printf("✅ Using existing target '%s': %s\n", name, existingID)
		// Return empty signing key, caller will preserve existing key from .env
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
func (c *Client) setExecution(condition *actionV2.Condition, targetID string) error {
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
// This is needed when migrating from V1 to V2, the old triggers won't fire
// with Login V2, so they should be cleaned up.
func (c *Client) cleanupActionsV1() {
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
func (c *Client) findActionByName(name string) (string, error) {
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
//
//	When a user authenticates via an external IdP, Zitadel sends the IdP's raw claims
//	(including group memberships) to our webhook. The webhook extracts the groups
//	and stores them as user metadata in Zitadel. This is provider-agnostic and works
//	with any OIDC provider (Azure AD, Okta, Cognito, Google, etc.).
//
// 2. Complement Token (Function preaccesstoken):
//
//	Before every access token is created, Zitadel sends the user's metadata to our webhook.
//	The webhook reads the sso_groups metadata and includes it as a custom claim in the JWT,
//	which StackWeaver then uses for automatic team assignment.
//
// Returns the signing keys for the created targets so they can be passed to the API server.
func (c *Client) ConfigureActions() (idpSyncKey, complementTokenKey string, err error) {
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

	// The API server webhook endpoint.
	// In Docker Compose (network_mode: host), Zitadel reaches the API at localhost:8022.
	// In Kubernetes, the Helm chart sets ACTIONS_API_BASE_URL to the in-cluster service URL.
	apiBaseURL := os.Getenv("ACTIONS_API_BASE_URL")
	if apiBaseURL == "" {
		apiBaseURL = "http://localhost:8022"
	}

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
