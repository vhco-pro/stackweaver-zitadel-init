// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/admin"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/policy"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/settings"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	otp      = policy.SecondFactorType_SECOND_FACTOR_TYPE_OTP
	u2f      = policy.SecondFactorType_SECOND_FACTOR_TYPE_U2F
	otpEmail = policy.SecondFactorType_SECOND_FACTOR_TYPE_OTP_EMAIL
	otpSMS   = policy.SecondFactorType_SECOND_FACTOR_TYPE_OTP_SMS
)

// fakeZitadelPolicies holds the instance policy, per-org policies (nil means the org inherits the
// instance default) and SMS providers, with Zitadel's no-op error behaviour.
type fakeZitadelPolicies struct {
	instance    *policy.LoginPolicy
	orgs        []string
	orgPolicies map[string]*policy.LoginPolicy
	smsActive   bool
	pageSize    int // ListOrgs page size the fake honours (to prove paging)
	updates     int
}

func orgIDFrom(ctx context.Context) string {
	md, _ := metadata.FromOutgoingContext(ctx)
	if v := md.Get("x-zitadel-orgid"); len(v) > 0 {
		return v[0]
	}
	return ""
}

func (f *fakeZitadelPolicies) target(ctx context.Context) *policy.LoginPolicy {
	if id := orgIDFrom(ctx); id != "" {
		return f.orgPolicies[id]
	}
	return f.instance
}

func applyUpdate(p *policy.LoginPolicy, allowRegister, hidePasswordReset bool, src proto.Message) error {
	if p.AllowRegister == allowRegister && p.HidePasswordReset == hidePasswordReset {
		return errors.New("rpc error: code = FailedPrecondition desc = Login Policy has not been changed (Errors.Instance.LoginPolicy.NotChanged)")
	}
	// Apply every field of the update request back onto the stored policy, so a field the
	// reconciler failed to copy shows up as a changed setting.
	s := src.ProtoReflect()
	d := p.ProtoReflect()
	fields := s.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if dfd := d.Descriptor().Fields().ByName(fd.Name()); dfd != nil {
			if s.Has(fd) {
				d.Set(dfd, s.Get(fd))
			} else {
				d.Clear(dfd)
			}
		}
	}
	return nil
}

func (f *fakeZitadelPolicies) addFactor(ctx context.Context, t policy.SecondFactorType) error {
	p := f.target(ctx)
	if slices.Contains(p.SecondFactors, t) {
		return errors.New("rpc error: code = AlreadyExists desc = Errors.Org.LoginPolicy.MFA.AlreadyExists")
	}
	p.SecondFactors = append(p.SecondFactors, t)
	return nil
}

func (f *fakeZitadelPolicies) removeFactor(ctx context.Context, t policy.SecondFactorType) error {
	p := f.target(ctx)
	i := slices.Index(p.SecondFactors, t)
	if i < 0 {
		return errors.New("rpc error: code = NotFound desc = Errors.Org.LoginPolicy.MFA.NotExisting")
	}
	p.SecondFactors = slices.Delete(p.SecondFactors, i, i+1)
	return nil
}

// --- admin (instance) side ---

type fakeInstance struct{ *fakeZitadelPolicies }

func (f fakeInstance) GetLoginPolicy(_ context.Context, _ *admin.GetLoginPolicyRequest, _ ...grpc.CallOption) (*admin.GetLoginPolicyResponse, error) {
	return &admin.GetLoginPolicyResponse{Policy: proto.Clone(f.instance).(*policy.LoginPolicy)}, nil
}

func (f fakeInstance) UpdateLoginPolicy(_ context.Context, in *admin.UpdateLoginPolicyRequest, _ ...grpc.CallOption) (*admin.UpdateLoginPolicyResponse, error) {
	if err := applyUpdate(f.instance, in.AllowRegister, in.HidePasswordReset, in); err != nil {
		return nil, err
	}
	f.updates++
	return &admin.UpdateLoginPolicyResponse{}, nil
}

func (f fakeInstance) AddSecondFactorToLoginPolicy(ctx context.Context, in *admin.AddSecondFactorToLoginPolicyRequest, _ ...grpc.CallOption) (*admin.AddSecondFactorToLoginPolicyResponse, error) {
	return &admin.AddSecondFactorToLoginPolicyResponse{}, f.addFactor(ctx, in.Type)
}

func (f fakeInstance) RemoveSecondFactorFromLoginPolicy(ctx context.Context, in *admin.RemoveSecondFactorFromLoginPolicyRequest, _ ...grpc.CallOption) (*admin.RemoveSecondFactorFromLoginPolicyResponse, error) {
	return &admin.RemoveSecondFactorFromLoginPolicyResponse{}, f.removeFactor(ctx, in.Type)
}

func (f fakeInstance) ListOrgs(_ context.Context, in *admin.ListOrgsRequest, _ ...grpc.CallOption) (*admin.ListOrgsResponse, error) {
	if in.GetQuery().GetLimit() == 0 {
		return nil, errors.New("ListOrgs must be paged")
	}
	limit := min(int(in.GetQuery().GetLimit()), f.pageSize)
	start := int(in.GetQuery().GetOffset())
	end := min(start+limit, len(f.orgs))
	var res []*org.Org
	for _, id := range f.orgs[min(start, end):end] {
		res = append(res, &org.Org{Id: id, Name: id})
	}
	return &admin.ListOrgsResponse{Result: res, Details: &object.ListDetails{TotalResult: uint64(len(f.orgs))}}, nil
}

func (f fakeInstance) ListSMSProviders(_ context.Context, _ *admin.ListSMSProvidersRequest, _ ...grpc.CallOption) (*admin.ListSMSProvidersResponse, error) {
	state := settings.SMSProviderConfigState_SMS_PROVIDER_CONFIG_INACTIVE
	if f.smsActive {
		state = settings.SMSProviderConfigState_SMS_PROVIDER_CONFIG_ACTIVE
	}
	return &admin.ListSMSProvidersResponse{Result: []*settings.SMSProvider{{Id: "sms-1", State: state}}}, nil
}

// --- management (org) side ---

type fakeOrgs struct{ *fakeZitadelPolicies }

func (f fakeOrgs) GetLoginPolicy(ctx context.Context, _ *management.GetLoginPolicyRequest, _ ...grpc.CallOption) (*management.GetLoginPolicyResponse, error) {
	id := orgIDFrom(ctx)
	if id == "" {
		return nil, errors.New("org-scoped call without an org ID")
	}
	if p := f.orgPolicies[id]; p != nil {
		return &management.GetLoginPolicyResponse{Policy: proto.Clone(p).(*policy.LoginPolicy), IsDefault: false}, nil
	}
	return &management.GetLoginPolicyResponse{Policy: proto.Clone(f.instance).(*policy.LoginPolicy), IsDefault: true}, nil
}

func (f fakeOrgs) UpdateCustomLoginPolicy(ctx context.Context, in *management.UpdateCustomLoginPolicyRequest, _ ...grpc.CallOption) (*management.UpdateCustomLoginPolicyResponse, error) {
	p := f.orgPolicies[orgIDFrom(ctx)]
	if p == nil {
		return nil, errors.New("updating a custom policy the org does not have")
	}
	if err := applyUpdate(p, in.AllowRegister, in.HidePasswordReset, in); err != nil {
		return nil, err
	}
	f.updates++
	return &management.UpdateCustomLoginPolicyResponse{}, nil
}

func (f fakeOrgs) AddSecondFactorToLoginPolicy(ctx context.Context, in *management.AddSecondFactorToLoginPolicyRequest, _ ...grpc.CallOption) (*management.AddSecondFactorToLoginPolicyResponse, error) {
	return &management.AddSecondFactorToLoginPolicyResponse{}, f.addFactor(ctx, in.Type)
}

func (f fakeOrgs) RemoveSecondFactorFromLoginPolicy(ctx context.Context, in *management.RemoveSecondFactorFromLoginPolicyRequest, _ ...grpc.CallOption) (*management.RemoveSecondFactorFromLoginPolicyResponse, error) {
	return &management.RemoveSecondFactorFromLoginPolicyResponse{}, f.removeFactor(ctx, in.Type)
}

// liveLikePolicies mirrors the dev Zitadel on 2026-10-02: an open instance policy with
// [OTP, U2F], an org with a custom policy and no second factors, and an org that inherits.
func liveLikePolicies() *fakeZitadelPolicies {
	return &fakeZitadelPolicies{
		instance: &policy.LoginPolicy{
			AllowUsernamePassword: true, AllowRegister: true, AllowExternalIdp: true,
			IgnoreUnknownUsernames: true, DefaultRedirectUri: "https://sw.example.com",
			PasswordCheckLifetime: durationpb.New(240 * 3600e9),
			SecondFactors:         []policy.SecondFactorType{otp, u2f},
		},
		orgs: []string{"zitadel-org", "iac-platform"},
		orgPolicies: map[string]*policy.LoginPolicy{
			"zitadel-org": {AllowUsernamePassword: true, AllowRegister: true, AllowDomainDiscovery: true},
		},
		pageSize: 1,
	}
}

func reconcile(t *testing.T, f *fakeZitadelPolicies, intent PolicyIntent) *logBuffer {
	t.Helper()
	logs := &logBuffer{}
	if err := ReconcileLoginPolicies(context.Background(), fakeInstance{f}, fakeOrgs{f}, intent, logs.logf); err != nil {
		t.Fatalf("ReconcileLoginPolicies: %v", err)
	}
	return logs
}

// AC8: without mail capability the instance and every custom org policy close registration,
// hide password reset and drop email and SMS codes (no SMS provider is active here).
func TestReconcileLoginPolicies_NoMail(t *testing.T) {
	f := liveLikePolicies()
	f.instance.SecondFactors = append(f.instance.SecondFactors, otpEmail, otpSMS)
	reconcile(t, f, PolicyIntent{MailCapable: false, SelfRegistration: false})

	for name, p := range map[string]*policy.LoginPolicy{"instance": f.instance, "zitadel-org": f.orgPolicies["zitadel-org"]} {
		if p.AllowRegister || !p.HidePasswordReset {
			t.Errorf("%s: allowRegister=%v hidePasswordReset=%v, want false/true", name, p.AllowRegister, p.HidePasswordReset)
		}
		if slices.Contains(p.SecondFactors, otpEmail) || slices.Contains(p.SecondFactors, otpSMS) {
			t.Errorf("%s: second factors %v still offer mail or SMS codes", name, p.SecondFactors)
		}
	}
	if !slices.Contains(f.instance.SecondFactors, otp) || !slices.Contains(f.instance.SecondFactors, u2f) {
		t.Errorf("factors that need no mail must stay: %v", f.instance.SecondFactors)
	}
}

// AC8/AC9/AC10: with mail, registration follows the operator's flag, reset is offered, and email
// codes are added where the policy already allows a second factor.
func TestReconcileLoginPolicies_WithMail(t *testing.T) {
	for _, open := range []bool{false, true} {
		f := liveLikePolicies()
		reconcile(t, f, PolicyIntent{MailCapable: true, SelfRegistration: open})
		for name, p := range map[string]*policy.LoginPolicy{"instance": f.instance, "zitadel-org": f.orgPolicies["zitadel-org"]} {
			if p.AllowRegister != open || p.HidePasswordReset {
				t.Errorf("selfRegistration=%v %s: allowRegister=%v hidePasswordReset=%v", open, name, p.AllowRegister, p.HidePasswordReset)
			}
		}
		if !slices.Contains(f.instance.SecondFactors, otpEmail) {
			t.Errorf("instance must offer email codes when mail works: %v", f.instance.SecondFactors)
		}
		if len(f.orgPolicies["zitadel-org"].SecondFactors) != 0 {
			t.Errorf("a policy with no second factor must not get email codes as its only one: %v", f.orgPolicies["zitadel-org"].SecondFactors)
		}
	}
}

// AC11: self-registration requested without mail stays closed and is reported.
func TestReconcileLoginPolicies_SelfRegistrationWithoutMailWarns(t *testing.T) {
	f := liveLikePolicies()
	logs := reconcile(t, f, PolicyIntent{MailCapable: false, SelfRegistration: true})
	if f.instance.AllowRegister {
		t.Fatal("registration must stay closed without mail")
	}
	if !containsLine(logs.lines, "SMTP_HOST") {
		t.Fatalf("the warning must name the missing SMTP setting, got %q", logs.lines)
	}
}

func TestReconcileLoginPolicies_SMSKeptWhenProviderActive(t *testing.T) {
	f := liveLikePolicies()
	f.smsActive = true
	f.instance.SecondFactors = append(f.instance.SecondFactors, otpSMS)
	reconcile(t, f, PolicyIntent{MailCapable: false})
	if !slices.Contains(f.instance.SecondFactors, otpSMS) {
		t.Fatal("SMS codes stay allowed while an SMS provider is active")
	}
}

// AC15: a second run changes nothing and succeeds (NotChanged and AlreadyExists are success), a
// console edit to a managed field is reverted, and every other field survives.
func TestReconcileLoginPolicies_IdempotentAndPreservesOtherFields(t *testing.T) {
	f := liveLikePolicies()
	intent := PolicyIntent{MailCapable: true, SelfRegistration: true}
	reconcile(t, f, intent)
	updates := f.updates
	reconcile(t, f, intent)
	if f.updates != updates {
		t.Errorf("a second run must not rewrite unchanged policies (%d extra updates)", f.updates-updates)
	}

	f.instance.HidePasswordReset = true // console edit
	reconcile(t, f, intent)
	if f.instance.HidePasswordReset {
		t.Error("a console edit to a managed field must be reverted")
	}
	if !f.instance.IgnoreUnknownUsernames || f.instance.DefaultRedirectUri != "https://sw.example.com" ||
		f.instance.PasswordCheckLifetime.AsDuration() != 240*3600e9 || !f.orgPolicies["zitadel-org"].AllowDomainDiscovery {
		t.Errorf("unrelated fields must survive: %+v / %+v", f.instance, f.orgPolicies["zitadel-org"])
	}
}

// AC15: the update calls replace the whole policy, so every field of both request types must be
// copied from the policy that was read. Fails when a zitadel-go upgrade adds a field.
func TestLoginPolicyUpdateRequestsCopyEveryField(t *testing.T) {
	src := &policy.LoginPolicy{}
	populate(t, src.ProtoReflect())
	for _, dst := range []proto.Message{&admin.UpdateLoginPolicyRequest{}, &management.UpdateCustomLoginPolicyRequest{}} {
		if err := copyLoginPolicy(dst, src); err != nil {
			t.Fatalf("%T: %v", dst, err)
		}
		m := dst.ProtoReflect()
		fields := m.Descriptor().Fields()
		for i := range fields.Len() {
			if fd := fields.Get(i); !m.Has(fd) {
				t.Errorf("%T.%s was not copied from the policy", dst, fd.Name())
			}
		}
	}
}

// populate sets every scalar, enum and message field of m to a non-default value.
func populate(t *testing.T, m protoreflect.Message) {
	t.Helper()
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.IsList() || fd.IsMap() {
			continue
		}
		switch fd.Kind() {
		case protoreflect.BoolKind:
			m.Set(fd, protoreflect.ValueOfBool(true))
		case protoreflect.StringKind:
			m.Set(fd, protoreflect.ValueOfString("x"))
		case protoreflect.EnumKind:
			m.Set(fd, protoreflect.ValueOfEnum(1))
		case protoreflect.MessageKind:
			if fd.Message().FullName() == "google.protobuf.Duration" {
				m.Set(fd, protoreflect.ValueOfMessage(durationpb.New(1e9).ProtoReflect()))
			}
		default:
			if !strings.HasPrefix(string(fd.Name()), "details") {
				t.Fatalf("populate does not handle %s (%s)", fd.Name(), fd.Kind())
			}
		}
	}
}
