// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"fmt"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/admin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// The lifetimes an instance gets when it has no OIDC settings at all, matching
// OIDCSettings in deploy/zitadel-defaults.yaml. Only the access-token lifetime is reconciled on an
// instance that has settings; the others belong to the operator once set.
const (
	defaultIDTokenLifetime            = 12 * time.Hour
	defaultRefreshTokenIdleExpiration = 720 * time.Hour
	defaultRefreshTokenExpiration     = 2160 * time.Hour
)

// OIDCSettingsAPI is the part of the Zitadel admin API the OIDC settings reconciler uses.
type OIDCSettingsAPI interface {
	GetOIDCSettings(ctx context.Context, in *admin.GetOIDCSettingsRequest, opts ...grpc.CallOption) (*admin.GetOIDCSettingsResponse, error)
	AddOIDCSettings(ctx context.Context, in *admin.AddOIDCSettingsRequest, opts ...grpc.CallOption) (*admin.AddOIDCSettingsResponse, error)
	UpdateOIDCSettings(ctx context.Context, in *admin.UpdateOIDCSettingsRequest, opts ...grpc.CallOption) (*admin.UpdateOIDCSettingsResponse, error)
}

// ReconcileOIDCSettings makes the instance's access-token lifetime match accessTokenLifetime.
// DefaultInstance in the Zitadel config only applies when an instance is created, so an existing
// install keeps whatever lifetime it was created with until this runs. The other three lifetimes
// are carried over unchanged. Zitadel refuses an update of absent settings and a no-op update, so
// the settings are read first: absent settings are added, matching settings are left alone.
func ReconcileOIDCSettings(ctx context.Context, api OIDCSettingsAPI, accessTokenLifetime time.Duration, logf func(format string, args ...any)) error {
	getCtx, cancel := context.WithTimeout(ctx, callTimeout)
	current, err := api.GetOIDCSettings(getCtx, &admin.GetOIDCSettingsRequest{})
	cancel()
	if status.Code(err) == codes.NotFound {
		addCtx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		if _, err := api.AddOIDCSettings(addCtx, &admin.AddOIDCSettingsRequest{
			AccessTokenLifetime:        durationpb.New(accessTokenLifetime),
			IdTokenLifetime:            durationpb.New(defaultIDTokenLifetime),
			RefreshTokenIdleExpiration: durationpb.New(defaultRefreshTokenIdleExpiration),
			RefreshTokenExpiration:     durationpb.New(defaultRefreshTokenExpiration),
		}); err != nil {
			return fmt.Errorf("adding OIDC settings: %w", err)
		}
		logf("OIDC settings created with a %s access-token lifetime", accessTokenLifetime)
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading OIDC settings: %w", err)
	}

	s := current.GetSettings()
	if s.GetAccessTokenLifetime().AsDuration() == accessTokenLifetime {
		return nil
	}
	updateCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if _, err := api.UpdateOIDCSettings(updateCtx, &admin.UpdateOIDCSettingsRequest{
		AccessTokenLifetime:        durationpb.New(accessTokenLifetime),
		IdTokenLifetime:            s.GetIdTokenLifetime(),
		RefreshTokenIdleExpiration: s.GetRefreshTokenIdleExpiration(),
		RefreshTokenExpiration:     s.GetRefreshTokenExpiration(),
	}); err != nil {
		return fmt.Errorf("updating OIDC settings: %w", err)
	}
	logf("OIDC access-token lifetime changed from %s to %s", s.GetAccessTokenLifetime().AsDuration(), accessTokenLifetime)
	return nil
}
