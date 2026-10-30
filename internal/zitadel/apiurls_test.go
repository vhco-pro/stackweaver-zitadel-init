// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"slices"
	"strings"
	"testing"

	actionV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/action/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	"google.golang.org/grpc"
)

// A fresh Compose install cannot save a URL on the api host: Zitadel resolves the host when the
// URL is saved, and the api container starts only after zitadel-init. The first run therefore
// creates everything without those URLs, and the api-urls run, once the API is healthy, adds them.

const (
	apiBase     = "http://api:8022"
	pendingBase = "http://zitadel:8080/stackweaver-api-pending"
)

// fakeMgmt answers the legacy Actions V1 cleanup ConfigureActions performs.
type fakeMgmt struct {
	management.ManagementServiceClient
}

func (fakeMgmt) SetTriggerActions(context.Context, *management.SetTriggerActionsRequest, ...grpc.CallOption) (*management.SetTriggerActionsResponse, error) {
	return &management.SetTriggerActionsResponse{}, nil
}

func (fakeMgmt) ListActions(context.Context, *management.ListActionsRequest, ...grpc.CallOption) (*management.ListActionsResponse, error) {
	return &management.ListActionsResponse{}, nil
}

func (f *fakeActions) SetExecution(context.Context, *actionV2.SetExecutionRequest, ...grpc.CallOption) (*actionV2.SetExecutionResponse, error) {
	f.executions++
	return &actionV2.SetExecutionResponse{}, nil
}

func actionsClient(f *fakeActions) *Client {
	return &Client{ctx: context.Background(), actionService: f, mgmtService: fakeMgmt{}}
}

func endpoints(f *fakeActions) []string {
	var out []string
	for _, t := range f.targets {
		out = append(out, t.GetEndpoint())
	}
	return out
}

// The first run creates both targets, so their signing keys reach .env before the API starts, but
// at an address Zitadel can save now rather than on the api host.
func TestConfigureActions_PendingCreatesTargetsAwayFromTheAPI(t *testing.T) {
	t.Setenv("AZURE_AD_CLIENT_ID", "azure-app")
	f := &fakeActions{}

	idpKey, tokenKey, err := actionsClient(f).ConfigureActions(ActionsOptions{BaseURL: apiBase, PendingBase: pendingBase})
	if err != nil {
		t.Fatalf("ConfigureActions: %v", err)
	}
	if f.created != 2 || idpKey == "" || tokenKey == "" {
		t.Fatalf("created %d targets, keys %q %q; want both targets and both signing keys", f.created, idpKey, tokenKey)
	}
	for _, e := range endpoints(f) {
		if !strings.HasPrefix(e, pendingBase+"/") {
			t.Errorf("target created at %q; the first run must not name the api host", e)
		}
	}
}

// A first run on an install whose targets already point at the API leaves them there.
func TestConfigureActions_PendingLeavesExistingTargetsAlone(t *testing.T) {
	t.Setenv("AZURE_AD_CLIENT_ID", "azure-app")
	f := &fakeActions{targets: []*actionV2.Target{
		{Id: "t1", Name: "stackweaver-idp-sync", Endpoint: apiBase + "/api/v2/zitadel/actions/idp-sync"},
		{Id: "t2", Name: "stackweaver-complement-token", Endpoint: apiBase + "/api/v2/zitadel/actions/complement-token"},
	}}
	if _, _, err := actionsClient(f).ConfigureActions(ActionsOptions{BaseURL: apiBase, PendingBase: pendingBase}); err != nil {
		t.Fatalf("ConfigureActions: %v", err)
	}
	if len(f.updates) != 0 || f.created != 0 {
		t.Fatalf("updates %d, created %d; existing targets must not move to the pending address", len(f.updates), f.created)
	}
}

// The api-urls run moves the targets to the API without rotating their keys, and a second run
// changes nothing.
func TestConfigureActions_APIURLsRunMovesTargetsOnceAndIsIdempotent(t *testing.T) {
	t.Setenv("OIDC_IDP_CLIENT_ID", "okta-app")
	f := &fakeActions{}
	c := actionsClient(f)
	if _, _, err := c.ConfigureActions(ActionsOptions{BaseURL: apiBase, PendingBase: pendingBase}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	idpKey, tokenKey, err := c.ConfigureActions(ActionsOptions{BaseURL: apiBase, RequireExisting: true})
	if err != nil {
		t.Fatalf("api-urls run: %v", err)
	}
	if idpKey != "" || tokenKey != "" || f.created != 2 {
		t.Fatalf("the api-urls run created targets or rotated keys (created %d, keys %q %q)", f.created, idpKey, tokenKey)
	}
	want := []string{apiBase + "/api/v2/zitadel/actions/idp-sync", apiBase + "/api/v2/zitadel/actions/complement-token"}
	if got := endpoints(f); !slices.Equal(got, want) {
		t.Fatalf("endpoints = %v, want %v", got, want)
	}
	for _, u := range f.updates {
		if u.ExpirationSigningKey != nil {
			t.Error("a target update rotates the signing key the API already holds")
		}
	}

	moved := len(f.updates)
	if _, _, err := c.ConfigureActions(ActionsOptions{BaseURL: apiBase, RequireExisting: true}); err != nil {
		t.Fatalf("second api-urls run: %v", err)
	}
	if len(f.updates) != moved || f.created != 2 {
		t.Fatalf("a second api-urls run changed something (updates %d -> %d, created %d)", moved, len(f.updates), f.created)
	}
}

// The api-urls run never creates a target: its signing key would not reach the running API.
func TestConfigureActions_APIURLsRunRefusesAMissingTarget(t *testing.T) {
	t.Setenv("AZURE_AD_CLIENT_ID", "azure-app")
	f := &fakeActions{}
	_, _, err := actionsClient(f).ConfigureActions(ActionsOptions{BaseURL: apiBase, RequireExisting: true})
	if err == nil || !strings.Contains(err.Error(), "stackweaver-idp-sync") {
		t.Fatalf("want an error naming the missing target, got %v", err)
	}
	if f.created != 0 {
		t.Fatal("the api-urls run created a target")
	}
}

// Without an external IdP there are no targets to configure, in either run.
func TestConfigureActions_NoIdPConfiguresNothing(t *testing.T) {
	t.Setenv("AZURE_AD_CLIENT_ID", "")
	t.Setenv("OIDC_IDP_CLIENT_ID", "")
	f := &fakeActions{}
	if _, _, err := actionsClient(f).ConfigureActions(ActionsOptions{BaseURL: apiBase, RequireExisting: true}); err != nil {
		t.Fatalf("ConfigureActions: %v", err)
	}
	if f.created != 0 || f.executions != 0 {
		t.Fatal("targets or executions were configured with no IdP")
	}
}

// The first run creates the BFF app without the back-channel URI; the api-urls run adds it and
// keeps every redirect, and a second api-urls run is a no-op.
func TestEnsureBFFBackChannelLogout_AddsTheURIOnceToAnAppCreatedWithout(t *testing.T) {
	f := newFakeApps()
	c := clientWithApps(f)
	if _, _, err := c.GetOrCreateBFFApp("proj", bffRedirect, bffPostLogout, "", false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := f.created.GetBackChannelLogoutUri(); got != "" {
		t.Fatalf("the first run set back-channel URI %q", got)
	}

	if err := c.EnsureBFFBackChannelLogout("proj", bffBackChan); err != nil {
		t.Fatalf("api-urls run: %v", err)
	}
	cfg := f.apps[bffAppName].GetOidcConfiguration()
	if cfg.GetBackChannelLogoutUri() != bffBackChan {
		t.Fatalf("back-channel URI = %q, want %q", cfg.GetBackChannelLogoutUri(), bffBackChan)
	}
	if !slices.Equal(cfg.GetRedirectUris(), bffRedirect) || !slices.Equal(cfg.GetPostLogoutRedirectUris(), bffPostLogout) {
		t.Fatalf("redirect URIs changed: %v / %v", cfg.GetRedirectUris(), cfg.GetPostLogoutRedirectUris())
	}

	before := f.updates
	if err := c.EnsureBFFBackChannelLogout("proj", bffBackChan); err != nil {
		t.Fatalf("second api-urls run: %v", err)
	}
	if f.updates != before {
		t.Fatalf("a second api-urls run updated the app again (%d -> %d updates)", before, f.updates)
	}
}

// The api-urls run reconciles an app zitadel-init made; it never creates one.
func TestEnsureBFFBackChannelLogout_RefusesAMissingApp(t *testing.T) {
	f := newFakeApps()
	if err := clientWithApps(f).EnsureBFFBackChannelLogout("proj", bffBackChan); err == nil {
		t.Fatal("want an error when the BFF app does not exist")
	}
	if f.created != nil {
		t.Fatal("the api-urls run created an app")
	}
}
