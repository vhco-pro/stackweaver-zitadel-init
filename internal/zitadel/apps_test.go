// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"slices"
	"strings"
	"testing"

	applicationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/application/v2"
	"google.golang.org/grpc"
)

// fakeApps is an in-memory ApplicationService holding apps by name. It embeds the interface so the
// calls the provisioning code does not make panic if they are ever reached.
type fakeApps struct {
	applicationV2.ApplicationServiceClient

	apps        map[string]*applicationV2.Application // by name
	created     *applicationV2.CreateOIDCApplicationRequest
	updated     *applicationV2.UpdateOIDCApplicationConfigurationRequest
	updates     int
	regenerated int
	deactivated []string
}

func newFakeApps() *fakeApps { return &fakeApps{apps: map[string]*applicationV2.Application{}} }

func nameFilter(req *applicationV2.ListApplicationsRequest) string {
	for _, f := range req.GetFilters() {
		if n := f.GetNameFilter(); n != nil {
			return n.GetName()
		}
	}
	return ""
}

func (f *fakeApps) ListApplications(_ context.Context, in *applicationV2.ListApplicationsRequest, _ ...grpc.CallOption) (*applicationV2.ListApplicationsResponse, error) {
	if app, ok := f.apps[nameFilter(in)]; ok {
		return &applicationV2.ListApplicationsResponse{Applications: []*applicationV2.Application{app}}, nil
	}
	return &applicationV2.ListApplicationsResponse{}, nil
}

func (f *fakeApps) byID(id string) *applicationV2.Application {
	for _, a := range f.apps {
		if a.GetApplicationId() == id {
			return a
		}
	}
	return nil
}

func (f *fakeApps) GetApplication(_ context.Context, in *applicationV2.GetApplicationRequest, _ ...grpc.CallOption) (*applicationV2.GetApplicationResponse, error) {
	return &applicationV2.GetApplicationResponse{Application: f.byID(in.GetApplicationId())}, nil
}

func (f *fakeApps) CreateApplication(_ context.Context, in *applicationV2.CreateApplicationRequest, _ ...grpc.CallOption) (*applicationV2.CreateApplicationResponse, error) {
	cfg := in.GetOidcConfiguration()
	f.created = cfg
	app := &applicationV2.Application{
		ApplicationId: "app-" + in.GetName(),
		Name:          in.GetName(),
		State:         applicationV2.ApplicationState_APPLICATION_STATE_ACTIVE,
		Configuration: &applicationV2.Application_OidcConfiguration{OidcConfiguration: &applicationV2.OIDCConfiguration{
			ClientId:                 "client-" + in.GetName(),
			RedirectUris:             cfg.GetRedirectUris(),
			PostLogoutRedirectUris:   cfg.GetPostLogoutRedirectUris(),
			IdTokenUserinfoAssertion: cfg.GetIdTokenUserinfoAssertion(),
			BackChannelLogoutUri:     cfg.GetBackChannelLogoutUri(),
			AuthMethodType:           cfg.GetAuthMethodType(),
			GrantTypes:               cfg.GetGrantTypes(),
			AccessTokenType:          cfg.GetAccessTokenType(),
		}},
	}
	f.apps[in.GetName()] = app
	return &applicationV2.CreateApplicationResponse{
		ApplicationId: app.GetApplicationId(),
		ApplicationType: &applicationV2.CreateApplicationResponse_OidcConfiguration{OidcConfiguration: &applicationV2.CreateOIDCApplicationResponse{
			ClientId: "client-" + in.GetName(), ClientSecret: "secret-at-create",
		}},
	}, nil
}

func (f *fakeApps) UpdateApplication(_ context.Context, in *applicationV2.UpdateApplicationRequest, _ ...grpc.CallOption) (*applicationV2.UpdateApplicationResponse, error) {
	u := in.GetOidcConfiguration()
	f.updated = u
	f.updates++
	// Apply the update, so a later run sees what an earlier one wrote, as it would in Zitadel.
	if a := f.byID(in.GetApplicationId()); a != nil && u != nil {
		cfg := a.GetOidcConfiguration()
		cfg.RedirectUris = u.GetRedirectUris()
		cfg.PostLogoutRedirectUris = u.GetPostLogoutRedirectUris()
		cfg.GrantTypes = u.GetGrantTypes()
		if u.AuthMethodType != nil {
			cfg.AuthMethodType = u.GetAuthMethodType()
		}
		if u.AccessTokenType != nil {
			cfg.AccessTokenType = u.GetAccessTokenType()
		}
		if u.IdTokenUserinfoAssertion != nil {
			cfg.IdTokenUserinfoAssertion = u.GetIdTokenUserinfoAssertion()
		}
		if u.BackChannelLogoutUri != nil {
			cfg.BackChannelLogoutUri = u.GetBackChannelLogoutUri()
		}
	}
	return &applicationV2.UpdateApplicationResponse{}, nil
}

func (f *fakeApps) GenerateClientSecret(_ context.Context, _ *applicationV2.GenerateClientSecretRequest, _ ...grpc.CallOption) (*applicationV2.GenerateClientSecretResponse, error) {
	f.regenerated++
	return &applicationV2.GenerateClientSecretResponse{ClientSecret: "secret-regenerated"}, nil
}

func (f *fakeApps) DeactivateApplication(_ context.Context, in *applicationV2.DeactivateApplicationRequest, _ ...grpc.CallOption) (*applicationV2.DeactivateApplicationResponse, error) {
	f.deactivated = append(f.deactivated, in.GetApplicationId())
	if a := f.byID(in.GetApplicationId()); a != nil {
		a.State = applicationV2.ApplicationState_APPLICATION_STATE_INACTIVE
	}
	return &applicationV2.DeactivateApplicationResponse{}, nil
}

func clientWithApps(f *fakeApps) *Client {
	return &Client{ctx: context.Background(), applicationService: f}
}

var (
	bffRedirect   = []string{"https://app.example.com/auth/callback"}
	bffPostLogout = []string{"https://app.example.com"}
	bffBackChan   = "https://app.example.com/auth/oidc/end-session"
)

// A new install gets a confidential web app: Basic client authentication, the authorization-code
// grant only (no refresh token, since the BFF never requests offline_access), JWT access tokens
// (for the sso_groups claim), ID-token userinfo assertion (so the code-flow ID token carries
// email_verified), and the back-channel logout URI. Its client id and secret come back.
func TestGetOrCreateBFFApp_CreatesAConfidentialClient(t *testing.T) {
	f := newFakeApps()
	id, secret, err := clientWithApps(f).GetOrCreateBFFApp("proj", bffRedirect, bffPostLogout, bffBackChan, false)
	if err != nil {
		t.Fatalf("GetOrCreateBFFApp: %v", err)
	}
	if id == "" || secret != "secret-at-create" {
		t.Fatalf("want the created client id and secret, got %q, %q", id, secret)
	}
	c := f.created
	if c == nil {
		t.Fatalf("no app was created")
	}
	if c.GetAuthMethodType() != applicationV2.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC {
		t.Errorf("auth method = %v, want BASIC", c.GetAuthMethodType())
	}
	if !slices.Equal(c.GetGrantTypes(), []applicationV2.OIDCGrantType{applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE}) {
		t.Errorf("grant types = %v, want the authorization-code grant only", c.GetGrantTypes())
	}
	if c.GetAccessTokenType() != applicationV2.OIDCTokenType_OIDC_TOKEN_TYPE_JWT {
		t.Errorf("access token type = %v, want JWT", c.GetAccessTokenType())
	}
	if !c.GetIdTokenUserinfoAssertion() {
		t.Errorf("ID-token userinfo assertion must be on")
	}
	if c.GetBackChannelLogoutUri() != bffBackChan {
		t.Errorf("back-channel URI = %q, want %q", c.GetBackChannelLogoutUri(), bffBackChan)
	}
	if !slices.Equal(c.GetRedirectUris(), bffRedirect) || !slices.Equal(c.GetPostLogoutRedirectUris(), bffPostLogout) {
		t.Errorf("redirect URIs not set as given")
	}
}

// An existing app with its secret already stored is reconciled, and no new secret is generated.
func TestGetOrCreateBFFApp_ReconcilesAnExistingApp(t *testing.T) {
	f := newFakeApps()
	if _, _, err := clientWithApps(f).GetOrCreateBFFApp("proj", bffRedirect, bffPostLogout, bffBackChan, false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	f.created = nil

	id, secret, err := clientWithApps(f).GetOrCreateBFFApp("proj", append(bffRedirect, "https://other.example.com/auth/callback"), bffPostLogout, bffBackChan, true)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if f.created != nil {
		t.Fatalf("an existing app must not be created again")
	}
	if id == "" || secret != "" {
		t.Fatalf("want the existing client id and no new secret, got %q, %q", id, secret)
	}
	if f.regenerated != 0 {
		t.Fatalf("a stored secret must not be regenerated")
	}
	if f.updated == nil || !slices.Contains(f.updated.GetRedirectUris(), "https://other.example.com/auth/callback") {
		t.Fatalf("a missing redirect URI must be added")
	}
	if f.updated.IdTokenUserinfoAssertion == nil || !*f.updated.IdTokenUserinfoAssertion {
		t.Errorf("reconcile must keep ID-token userinfo assertion on")
	}
	if f.updated.BackChannelLogoutUri == nil || *f.updated.BackChannelLogoutUri != bffBackChan {
		t.Errorf("reconcile must keep the back-channel URI")
	}
}

// With no back-channel URI configured (the Compose example), reconciling keeps the URI the app has,
// which an operator may have set in the console, instead of clearing it.
func TestGetOrCreateBFFApp_KeepsAConsoleBackChannelURI(t *testing.T) {
	f := newFakeApps()
	if _, _, err := clientWithApps(f).GetOrCreateBFFApp("proj", bffRedirect, bffPostLogout, bffBackChan, false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, _, err := clientWithApps(f).GetOrCreateBFFApp("proj", append(bffRedirect, "https://other.example.com/auth/callback"), bffPostLogout, "", true); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if f.updated == nil || f.updated.BackChannelLogoutUri == nil || *f.updated.BackChannelLogoutUri != bffBackChan {
		t.Fatalf("an empty setting must keep the app's back-channel URI %q", bffBackChan)
	}
}

// The Compose installs run on localhost and get its redirect; a deployment that names its own
// redirect (the Helm chart) gets none, so an https install's app is not in development mode.
func TestDefaultRedirectURIs_LocalhostOnlyWhenAsked(t *testing.T) {
	r, p := DefaultRedirectURIs([]string{"https://sw.example.com/auth/callback"}, []string{"https://sw.example.com"}, false)
	if slices.ContainsFunc(append(r, p...), func(u string) bool { return strings.Contains(u, "localhost") }) {
		t.Fatalf("a deployment with its own redirect got a localhost URI: %v %v", r, p)
	}
	if usesPlainHTTP(r, p) {
		t.Fatalf("an https install's app would be put in development mode")
	}
	f := newFakeApps()
	if _, _, err := clientWithApps(f).GetOrCreateBFFApp("proj", r, p, bffBackChan, false); err != nil {
		t.Fatalf("GetOrCreateBFFApp: %v", err)
	}
	if f.created.GetDevelopmentMode() {
		t.Fatalf("the https install's app was created in development mode")
	}

	r, p = DefaultRedirectURIs(nil, nil, true)
	if !slices.Contains(r, "http://localhost:5173/auth/callback") || !slices.Contains(p, "http://localhost:5173") {
		t.Fatalf("a Compose install lost its localhost redirect: %v %v", r, p)
	}
}

// Zitadel returns a client secret only when the app is created, so an existing app whose secret
// is not stored gets a new one generated.
func TestGetOrCreateBFFApp_RegeneratesAMissingSecret(t *testing.T) {
	f := newFakeApps()
	if _, _, err := clientWithApps(f).GetOrCreateBFFApp("proj", bffRedirect, bffPostLogout, bffBackChan, false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	_, secret, err := clientWithApps(f).GetOrCreateBFFApp("proj", bffRedirect, bffPostLogout, bffBackChan, false)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if f.regenerated != 1 || secret != "secret-regenerated" {
		t.Fatalf("want one regenerated secret returned, got %d regenerations and %q", f.regenerated, secret)
	}
}

// The public SPA client is deactivated, once, so nothing on the SPA's origin can redeem a code
// for tokens any more. A missing app is not an error.
func TestDeactivatePublicFrontendApp(t *testing.T) {
	f := newFakeApps()
	if err := clientWithApps(f).DeactivatePublicFrontendApp("proj"); err != nil {
		t.Fatalf("with no public app: %v", err)
	}
	f.apps[frontendAppName] = &applicationV2.Application{
		ApplicationId: "app-public", Name: frontendAppName, State: applicationV2.ApplicationState_APPLICATION_STATE_ACTIVE,
	}
	for range 2 {
		if err := clientWithApps(f).DeactivatePublicFrontendApp("proj"); err != nil {
			t.Fatalf("DeactivatePublicFrontendApp: %v", err)
		}
	}
	if !slices.Equal(f.deactivated, []string{"app-public"}) {
		t.Fatalf("want the public app deactivated exactly once, got %v", f.deactivated)
	}
}
