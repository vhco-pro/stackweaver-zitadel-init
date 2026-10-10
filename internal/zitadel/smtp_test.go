// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/config"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/admin"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/settings"
	"google.golang.org/grpc"
)

// fakeEmailProviders is an in-memory Zitadel email-provider store with Zitadel's rule that only
// one provider is active at a time.
type fakeEmailProviders struct {
	providers []*settings.EmailProvider
	passwords map[string]string // id -> last password sent
	auth      map[string]string // id -> "plain" or "none"
	nextID    int
	calls     []string
}

func newFakeEmailProviders(existing ...*settings.EmailProvider) *fakeEmailProviders {
	return &fakeEmailProviders{providers: existing, passwords: map[string]string{}, auth: map[string]string{}}
}

func (f *fakeEmailProviders) find(id string) *settings.EmailProvider {
	for _, p := range f.providers {
		if p.Id == id {
			return p
		}
	}
	return nil
}

func (f *fakeEmailProviders) ListEmailProviders(_ context.Context, _ *admin.ListEmailProvidersRequest, _ ...grpc.CallOption) (*admin.ListEmailProvidersResponse, error) {
	f.calls = append(f.calls, "list")
	return &admin.ListEmailProvidersResponse{Result: f.providers}, nil
}

func smtpOf(host, sender, user string, tls bool) *settings.EmailProvider_Smtp {
	return &settings.EmailProvider_Smtp{Smtp: &settings.EmailProviderSMTP{Host: host, SenderAddress: sender, User: user, Tls: tls}}
}

func (f *fakeEmailProviders) AddEmailProviderSMTP(_ context.Context, in *admin.AddEmailProviderSMTPRequest, _ ...grpc.CallOption) (*admin.AddEmailProviderSMTPResponse, error) {
	f.calls = append(f.calls, "add")
	f.nextID++
	id := fmt.Sprintf("new-%d", f.nextID)
	f.providers = append(f.providers, &settings.EmailProvider{
		Id: id, Description: in.Description, State: settings.EmailProviderState_EMAIL_PROVIDER_INACTIVE,
		Config: smtpOf(in.Host, in.SenderAddress, in.User, in.Tls),
	})
	f.recordAuth(id, in.GetPlain(), in.GetNone() != nil)
	return &admin.AddEmailProviderSMTPResponse{Id: id}, nil
}

func (f *fakeEmailProviders) UpdateEmailProviderSMTP(_ context.Context, in *admin.UpdateEmailProviderSMTPRequest, _ ...grpc.CallOption) (*admin.UpdateEmailProviderSMTPResponse, error) {
	f.calls = append(f.calls, "update")
	p := f.find(in.Id)
	if p == nil {
		return nil, errors.New("not found")
	}
	p.Description = in.Description
	p.Config = smtpOf(in.Host, in.SenderAddress, in.User, in.Tls)
	f.recordAuth(in.Id, in.GetPlain(), in.GetNone() != nil)
	return &admin.UpdateEmailProviderSMTPResponse{}, nil
}

func (f *fakeEmailProviders) recordAuth(id string, plain *admin.SMTPPlainAuth, none bool) {
	switch {
	case plain != nil:
		f.auth[id] = "plain"
		f.passwords[id] = plain.Password
	case none:
		f.auth[id] = "none"
	default:
		f.auth[id] = "unset"
	}
}

func (f *fakeEmailProviders) ActivateEmailProvider(_ context.Context, in *admin.ActivateEmailProviderRequest, _ ...grpc.CallOption) (*admin.ActivateEmailProviderResponse, error) {
	f.calls = append(f.calls, "activate")
	p := f.find(in.Id)
	if p.State == settings.EmailProviderState_EMAIL_PROVIDER_ACTIVE {
		return nil, errors.New("rpc error: code = FailedPrecondition desc = SMTP configuration is already active (Errors.SMTPConfig.AlreadyActive)")
	}
	for _, other := range f.providers {
		other.State = settings.EmailProviderState_EMAIL_PROVIDER_INACTIVE
	}
	p.State = settings.EmailProviderState_EMAIL_PROVIDER_ACTIVE
	return &admin.ActivateEmailProviderResponse{}, nil
}

func (f *fakeEmailProviders) DeactivateEmailProvider(_ context.Context, in *admin.DeactivateEmailProviderRequest, _ ...grpc.CallOption) (*admin.DeactivateEmailProviderResponse, error) {
	f.calls = append(f.calls, "deactivate")
	p := f.find(in.Id)
	if p.State != settings.EmailProviderState_EMAIL_PROVIDER_ACTIVE {
		return nil, errors.New("rpc error: code = FailedPrecondition desc = SMTP configuration is already deactivated (Errors.SMTPConfig.AlreadyDeactivated)")
	}
	p.State = settings.EmailProviderState_EMAIL_PROVIDER_INACTIVE
	return &admin.DeactivateEmailProviderResponse{}, nil
}

func (f *fakeEmailProviders) managed() []*settings.EmailProvider {
	var out []*settings.EmailProvider
	for _, p := range f.providers {
		if p.Description == ManagedEmailProviderDescription {
			out = append(out, p)
		}
	}
	return out
}

type logBuffer struct{ lines []string }

func (l *logBuffer) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

var resendCfg = config.SMTPConfig{
	Host: "smtp.resend.com:587", User: "resend", Password: "re_secret_one",
	SenderAddress: "noreply@mail.example.com", SenderName: "Stackweaver", TLS: true,
}

// AC1: the first run creates and activates exactly one managed provider; a second run keeps
// exactly one with the same settings and exits cleanly; a rotated password is sent.
func TestReconcileSMTP_CreatesThenStaysSingle(t *testing.T) {
	ctx := context.Background()
	fake := newFakeEmailProviders()
	logs := &logBuffer{}

	active, err := ReconcileSMTP(ctx, fake, resendCfg, logs.logf)
	if err != nil || !active {
		t.Fatalf("first run: active=%v err=%v", active, err)
	}
	if got := fake.managed(); len(got) != 1 || got[0].State != settings.EmailProviderState_EMAIL_PROVIDER_ACTIVE {
		t.Fatalf("want one active managed provider, got %+v", got)
	}
	id := fake.managed()[0].Id
	if fake.auth[id] != "plain" || fake.passwords[id] != "re_secret_one" {
		t.Fatalf("password must be sent in Auth.Plain, got auth=%q password=%q", fake.auth[id], fake.passwords[id])
	}

	if active, err = ReconcileSMTP(ctx, fake, resendCfg, logs.logf); err != nil || !active {
		t.Fatalf("second run must succeed quietly: active=%v err=%v", active, err)
	}
	if got := fake.managed(); len(got) != 1 {
		t.Fatalf("second run must not create a duplicate, got %d managed providers", len(got))
	}
	smtp := fake.managed()[0].GetSmtp()
	if smtp.Host != resendCfg.Host || smtp.SenderAddress != resendCfg.SenderAddress || smtp.User != resendCfg.User || !smtp.Tls {
		t.Fatalf("settings drifted: %+v", smtp)
	}

	rotated := resendCfg
	rotated.Password = "re_secret_two"
	if _, err := ReconcileSMTP(ctx, fake, rotated, logs.logf); err != nil {
		t.Fatal(err)
	}
	if fake.passwords[id] != "re_secret_two" {
		t.Fatalf("a rotated password must reach Zitadel, got %q", fake.passwords[id])
	}
	for _, line := range logs.lines {
		if strings.Contains(line, "re_secret") {
			t.Fatalf("a log line leaks the SMTP password: %q", line)
		}
	}
}

func TestReconcileSMTP_NoUserMeansNoAuth(t *testing.T) {
	fake := newFakeEmailProviders()
	mailpit := config.SMTPConfig{Host: "mailpit:1025", SenderAddress: "noreply@stackweaver.local", SenderName: "Stackweaver"}
	if _, err := ReconcileSMTP(context.Background(), fake, mailpit, (&logBuffer{}).logf); err != nil {
		t.Fatal(err)
	}
	if got := fake.auth[fake.managed()[0].Id]; got != "none" {
		t.Fatalf("a provider without SMTP_USER must use Auth.None, got %q", got)
	}
}

// AC3 + D2: activating the managed provider supersedes an operator-made one (Zitadel allows one
// active provider) and logs it; removing SMTP_HOST deactivates only the managed provider.
func TestReconcileSMTP_OperatorProvider(t *testing.T) {
	ctx := context.Background()
	operator := &settings.EmailProvider{
		Id: "op-1", Description: "Corporate relay", State: settings.EmailProviderState_EMAIL_PROVIDER_ACTIVE,
		Config: smtpOf("relay.corp:25", "it@corp.example", "", false),
	}
	fake := newFakeEmailProviders(operator)
	logs := &logBuffer{}

	if _, err := ReconcileSMTP(ctx, fake, resendCfg, logs.logf); err != nil {
		t.Fatal(err)
	}
	if operator.State != settings.EmailProviderState_EMAIL_PROVIDER_INACTIVE {
		t.Fatal("activating the managed provider must supersede the operator's")
	}
	if operator.Description != "Corporate relay" || operator.GetSmtp().Host != "relay.corp:25" {
		t.Fatalf("the operator's provider must otherwise be untouched: %+v", operator)
	}
	if !containsLine(logs.lines, "Corporate relay") {
		t.Fatalf("the superseded provider must be logged, got %q", logs.lines)
	}

	// Operator re-activates their own provider in the console, then SMTP_HOST is removed.
	operator.State = settings.EmailProviderState_EMAIL_PROVIDER_ACTIVE
	fake.managed()[0].State = settings.EmailProviderState_EMAIL_PROVIDER_INACTIVE
	active, err := ReconcileSMTP(ctx, fake, config.SMTPConfig{}, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("an active operator provider still means mail is deliverable")
	}
	if operator.State != settings.EmailProviderState_EMAIL_PROVIDER_ACTIVE {
		t.Fatal("unset SMTP_HOST must not touch an operator provider")
	}
}

func TestReconcileSMTP_UnsetHostDeactivatesManaged(t *testing.T) {
	ctx := context.Background()
	fake := newFakeEmailProviders()
	if _, err := ReconcileSMTP(ctx, fake, resendCfg, (&logBuffer{}).logf); err != nil {
		t.Fatal(err)
	}
	active, err := ReconcileSMTP(ctx, fake, config.SMTPConfig{}, (&logBuffer{}).logf)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("with the managed provider deactivated and no other provider, mail is not deliverable")
	}
	if fake.managed()[0].State != settings.EmailProviderState_EMAIL_PROVIDER_INACTIVE {
		t.Fatal("removing SMTP_HOST must deactivate the managed provider")
	}
	// A further run with nothing to deactivate is a quiet no-op.
	if _, err := ReconcileSMTP(ctx, fake, config.SMTPConfig{}, (&logBuffer{}).logf); err != nil {
		t.Fatalf("an already-inactive managed provider must not fail the run: %v", err)
	}
}

func containsLine(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
