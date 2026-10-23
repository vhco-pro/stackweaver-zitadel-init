// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"testing"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/admin"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/settings"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// fakeOIDCSettings is an instance's OIDC settings with Zitadel's rules: reading or changing absent
// settings answers NotFound, and adding settings that exist answers AlreadyExists.
type fakeOIDCSettings struct {
	current *settings.OIDCSettings
	adds    []*admin.AddOIDCSettingsRequest
	updates []*admin.UpdateOIDCSettingsRequest
}

func (f *fakeOIDCSettings) GetOIDCSettings(_ context.Context, _ *admin.GetOIDCSettingsRequest, _ ...grpc.CallOption) (*admin.GetOIDCSettingsResponse, error) {
	if f.current == nil {
		return nil, status.Error(codes.NotFound, "Errors.OIDCSettings.NotFound")
	}
	return &admin.GetOIDCSettingsResponse{Settings: f.current}, nil
}

func (f *fakeOIDCSettings) AddOIDCSettings(_ context.Context, in *admin.AddOIDCSettingsRequest, _ ...grpc.CallOption) (*admin.AddOIDCSettingsResponse, error) {
	if f.current != nil {
		return nil, status.Error(codes.AlreadyExists, "Errors.OIDCSettings.AlreadyExists")
	}
	f.adds = append(f.adds, in)
	f.current = &settings.OIDCSettings{
		AccessTokenLifetime:        in.GetAccessTokenLifetime(),
		IdTokenLifetime:            in.GetIdTokenLifetime(),
		RefreshTokenIdleExpiration: in.GetRefreshTokenIdleExpiration(),
		RefreshTokenExpiration:     in.GetRefreshTokenExpiration(),
	}
	return &admin.AddOIDCSettingsResponse{}, nil
}

func (f *fakeOIDCSettings) UpdateOIDCSettings(_ context.Context, in *admin.UpdateOIDCSettingsRequest, _ ...grpc.CallOption) (*admin.UpdateOIDCSettingsResponse, error) {
	if f.current == nil {
		return nil, status.Error(codes.NotFound, "Errors.OIDCSettings.NotFound")
	}
	f.updates = append(f.updates, in)
	f.current = &settings.OIDCSettings{
		AccessTokenLifetime:        in.GetAccessTokenLifetime(),
		IdTokenLifetime:            in.GetIdTokenLifetime(),
		RefreshTokenIdleExpiration: in.GetRefreshTokenIdleExpiration(),
		RefreshTokenExpiration:     in.GetRefreshTokenExpiration(),
	}
	return &admin.UpdateOIDCSettingsResponse{}, nil
}

func installedOIDCSettings(access time.Duration) *settings.OIDCSettings {
	return &settings.OIDCSettings{
		AccessTokenLifetime:        durationpb.New(access),
		IdTokenLifetime:            durationpb.New(12 * time.Hour),
		RefreshTokenIdleExpiration: durationpb.New(720 * time.Hour),
		RefreshTokenExpiration:     durationpb.New(2160 * time.Hour),
	}
}

func discardLog(string, ...any) {}

// AC6: an existing install at 12 hours is brought to one hour, keeping its other lifetimes.
func TestReconcileOIDCSettings_ShortensAnExistingLifetime(t *testing.T) {
	f := &fakeOIDCSettings{current: installedOIDCSettings(12 * time.Hour)}

	if err := ReconcileOIDCSettings(context.Background(), f, time.Hour, discardLog); err != nil {
		t.Fatalf("ReconcileOIDCSettings: %v", err)
	}
	if len(f.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(f.updates))
	}
	got := f.current
	if got.GetAccessTokenLifetime().AsDuration() != time.Hour {
		t.Errorf("access token lifetime = %v, want 1h", got.GetAccessTokenLifetime().AsDuration())
	}
	if got.GetIdTokenLifetime().AsDuration() != 12*time.Hour ||
		got.GetRefreshTokenIdleExpiration().AsDuration() != 720*time.Hour ||
		got.GetRefreshTokenExpiration().AsDuration() != 2160*time.Hour {
		t.Errorf("other lifetimes changed: id %v, refresh idle %v, refresh %v",
			got.GetIdTokenLifetime().AsDuration(), got.GetRefreshTokenIdleExpiration().AsDuration(), got.GetRefreshTokenExpiration().AsDuration())
	}
}

// An instance already at the configured lifetime is left alone: Zitadel refuses a no-op update.
func TestReconcileOIDCSettings_LeavesAMatchingLifetimeAlone(t *testing.T) {
	f := &fakeOIDCSettings{current: installedOIDCSettings(time.Hour)}

	if err := ReconcileOIDCSettings(context.Background(), f, time.Hour, discardLog); err != nil {
		t.Fatalf("ReconcileOIDCSettings: %v", err)
	}
	if len(f.updates) != 0 || len(f.adds) != 0 {
		t.Errorf("wrote settings that already matched: %d updates, %d adds", len(f.updates), len(f.adds))
	}
}

// AC14: an instance with no OIDC settings gets them, with the configured access-token lifetime and
// the platform's other defaults.
func TestReconcileOIDCSettings_CreatesAbsentSettings(t *testing.T) {
	f := &fakeOIDCSettings{}

	if err := ReconcileOIDCSettings(context.Background(), f, time.Hour, discardLog); err != nil {
		t.Fatalf("ReconcileOIDCSettings: %v", err)
	}
	if len(f.adds) != 1 {
		t.Fatalf("adds = %d, want 1", len(f.adds))
	}
	add := f.adds[0]
	if add.GetAccessTokenLifetime().AsDuration() != time.Hour {
		t.Errorf("added access token lifetime = %v, want 1h", add.GetAccessTokenLifetime().AsDuration())
	}
	if add.GetIdTokenLifetime().AsDuration() != 12*time.Hour ||
		add.GetRefreshTokenIdleExpiration().AsDuration() != 720*time.Hour ||
		add.GetRefreshTokenExpiration().AsDuration() != 2160*time.Hour {
		t.Errorf("added defaults: id %v, refresh idle %v, refresh %v; want 12h, 720h, 2160h",
			add.GetIdTokenLifetime().AsDuration(), add.GetRefreshTokenIdleExpiration().AsDuration(), add.GetRefreshTokenExpiration().AsDuration())
	}
}
