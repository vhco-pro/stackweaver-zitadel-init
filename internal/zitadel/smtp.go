// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/config"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/admin"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/settings"
	"google.golang.org/grpc"
)

// ManagedEmailProviderDescription marks the Zitadel email provider that zitadel-init owns.
// Providers without it belong to the operator and are only ever superseded, never edited.
const ManagedEmailProviderDescription = "stackweaver-managed"

// callTimeout bounds every Zitadel call a reconciler makes, so a hung connection fails the run
// instead of idling the container forever.
const callTimeout = 15 * time.Second

// EmailProviders is the part of the Zitadel admin API the SMTP reconciler uses.
type EmailProviders interface {
	ListEmailProviders(ctx context.Context, in *admin.ListEmailProvidersRequest, opts ...grpc.CallOption) (*admin.ListEmailProvidersResponse, error)
	AddEmailProviderSMTP(ctx context.Context, in *admin.AddEmailProviderSMTPRequest, opts ...grpc.CallOption) (*admin.AddEmailProviderSMTPResponse, error)
	UpdateEmailProviderSMTP(ctx context.Context, in *admin.UpdateEmailProviderSMTPRequest, opts ...grpc.CallOption) (*admin.UpdateEmailProviderSMTPResponse, error)
	ActivateEmailProvider(ctx context.Context, in *admin.ActivateEmailProviderRequest, opts ...grpc.CallOption) (*admin.ActivateEmailProviderResponse, error)
	DeactivateEmailProvider(ctx context.Context, in *admin.DeactivateEmailProviderRequest, opts ...grpc.CallOption) (*admin.DeactivateEmailProviderResponse, error)
}

// ReconcileSMTP makes Zitadel's email provider match cfg (#829 D2) and reports whether any
// provider is active afterwards, which is the SMTP half of mail capability.
//
// With SMTP configured, the managed provider is created or updated and activated. Zitadel never
// returns a stored password, so it is sent on every run, which picks up a rotated password.
// Zitadel allows one active provider, so activation supersedes an operator's provider, which is
// logged and otherwise left alone. Without SMTP configured, only the managed provider is
// deactivated; a provider the operator set up in the console keeps working.
func ReconcileSMTP(ctx context.Context, api EmailProviders, cfg config.SMTPConfig, logf func(format string, args ...any)) (bool, error) {
	listCtx, cancel := context.WithTimeout(ctx, callTimeout)
	list, err := api.ListEmailProviders(listCtx, &admin.ListEmailProvidersRequest{})
	cancel()
	if err != nil {
		return false, fmt.Errorf("listing email providers: %w", err)
	}

	var managed *settings.EmailProvider
	var operatorActive []*settings.EmailProvider
	for _, p := range list.GetResult() {
		switch {
		case p.GetDescription() == ManagedEmailProviderDescription && managed == nil:
			managed = p
		case p.GetState() == settings.EmailProviderState_EMAIL_PROVIDER_ACTIVE:
			operatorActive = append(operatorActive, p)
		}
	}

	if !cfg.Enabled() {
		if managed != nil && managed.GetState() == settings.EmailProviderState_EMAIL_PROVIDER_ACTIVE {
			if err := deactivateEmailProvider(ctx, api, managed.GetId()); err != nil {
				return false, err
			}
			logf("SMTP_HOST is not set: deactivated the managed email provider")
		}
		return len(operatorActive) > 0, nil
	}

	id, err := upsertManagedSMTP(ctx, api, managed, cfg)
	if err != nil {
		return false, err
	}
	if managed == nil || managed.GetState() != settings.EmailProviderState_EMAIL_PROVIDER_ACTIVE {
		if err := activateEmailProvider(ctx, api, id); err != nil {
			return false, err
		}
		for _, p := range operatorActive {
			logf("activated the managed email provider; it supersedes %q (%s), which is now inactive", p.GetDescription(), p.GetId())
		}
	}
	logf("email provider: %s", cfg)
	return true, nil
}

func upsertManagedSMTP(ctx context.Context, api EmailProviders, managed *settings.EmailProvider, cfg config.SMTPConfig) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if managed == nil {
		req := &admin.AddEmailProviderSMTPRequest{
			SenderAddress: cfg.SenderAddress,
			SenderName:    cfg.SenderName,
			Tls:           cfg.TLS,
			Host:          cfg.Host,
			User:          cfg.User,
			Description:   ManagedEmailProviderDescription,
		}
		if cfg.User != "" {
			req.Auth = &admin.AddEmailProviderSMTPRequest_Plain{Plain: &admin.SMTPPlainAuth{Password: cfg.Password}}
		} else {
			req.Auth = &admin.AddEmailProviderSMTPRequest_None{None: &admin.SMTPNoAuth{}}
		}
		resp, err := api.AddEmailProviderSMTP(callCtx, req)
		if err != nil {
			return "", fmt.Errorf("adding the managed email provider: %w", err)
		}
		return resp.GetId(), nil
	}
	req := &admin.UpdateEmailProviderSMTPRequest{
		Id:            managed.GetId(),
		SenderAddress: cfg.SenderAddress,
		SenderName:    cfg.SenderName,
		Tls:           cfg.TLS,
		Host:          cfg.Host,
		User:          cfg.User,
		Description:   ManagedEmailProviderDescription,
	}
	if cfg.User != "" {
		req.Auth = &admin.UpdateEmailProviderSMTPRequest_Plain{Plain: &admin.SMTPPlainAuth{Password: cfg.Password}}
	} else {
		req.Auth = &admin.UpdateEmailProviderSMTPRequest_None{None: &admin.SMTPNoAuth{}}
	}
	if _, err := api.UpdateEmailProviderSMTP(callCtx, req); err != nil {
		return "", fmt.Errorf("updating the managed email provider %s: %w", managed.GetId(), err)
	}
	return managed.GetId(), nil
}

func activateEmailProvider(ctx context.Context, api EmailProviders, id string) error {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if _, err := api.ActivateEmailProvider(callCtx, &admin.ActivateEmailProviderRequest{Id: id}); err != nil && !strings.Contains(err.Error(), "AlreadyActive") {
		return fmt.Errorf("activating email provider %s: %w", id, err)
	}
	return nil
}

func deactivateEmailProvider(ctx context.Context, api EmailProviders, id string) error {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if _, err := api.DeactivateEmailProvider(callCtx, &admin.DeactivateEmailProviderRequest{Id: id}); err != nil && !strings.Contains(err.Error(), "AlreadyDeactivated") {
		return fmt.Errorf("deactivating email provider %s: %w", id, err)
	}
	return nil
}
