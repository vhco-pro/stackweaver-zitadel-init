// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zitadel/zitadel-go/v3/pkg/client/middleware"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/admin"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/policy"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/settings"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// InstancePolicies is the part of the Zitadel admin API the login-policy reconciler uses.
type InstancePolicies interface {
	GetLoginPolicy(ctx context.Context, in *admin.GetLoginPolicyRequest, opts ...grpc.CallOption) (*admin.GetLoginPolicyResponse, error)
	UpdateLoginPolicy(ctx context.Context, in *admin.UpdateLoginPolicyRequest, opts ...grpc.CallOption) (*admin.UpdateLoginPolicyResponse, error)
	AddSecondFactorToLoginPolicy(ctx context.Context, in *admin.AddSecondFactorToLoginPolicyRequest, opts ...grpc.CallOption) (*admin.AddSecondFactorToLoginPolicyResponse, error)
	RemoveSecondFactorFromLoginPolicy(ctx context.Context, in *admin.RemoveSecondFactorFromLoginPolicyRequest, opts ...grpc.CallOption) (*admin.RemoveSecondFactorFromLoginPolicyResponse, error)
	ListOrgs(ctx context.Context, in *admin.ListOrgsRequest, opts ...grpc.CallOption) (*admin.ListOrgsResponse, error)
	ListSMSProviders(ctx context.Context, in *admin.ListSMSProvidersRequest, opts ...grpc.CallOption) (*admin.ListSMSProvidersResponse, error)
}

// OrgPolicies is the part of the Zitadel management API the reconciler uses for org policies.
// Every call carries the org ID through middleware.SetOrgID.
type OrgPolicies interface {
	GetLoginPolicy(ctx context.Context, in *management.GetLoginPolicyRequest, opts ...grpc.CallOption) (*management.GetLoginPolicyResponse, error)
	UpdateCustomLoginPolicy(ctx context.Context, in *management.UpdateCustomLoginPolicyRequest, opts ...grpc.CallOption) (*management.UpdateCustomLoginPolicyResponse, error)
	AddSecondFactorToLoginPolicy(ctx context.Context, in *management.AddSecondFactorToLoginPolicyRequest, opts ...grpc.CallOption) (*management.AddSecondFactorToLoginPolicyResponse, error)
	RemoveSecondFactorFromLoginPolicy(ctx context.Context, in *management.RemoveSecondFactorFromLoginPolicyRequest, opts ...grpc.CallOption) (*management.RemoveSecondFactorFromLoginPolicyResponse, error)
}

// PolicyIntent is what the login policy must express (#829 D4).
type PolicyIntent struct {
	MailCapable      bool // codes can reach users (see MailCapable)
	SelfRegistration bool // the operator wants open signup
}

const orgPageSize = 100

// ReconcileLoginPolicies applies the mail-dependent login settings to the instance default
// policy and to every org with a custom policy (#829 D4). Only these settings are managed:
// allowRegister, hidePasswordReset, and the email and SMS second factors. Every other field is
// read and written back unchanged. Zitadel's "not changed" and "already exists" answers count
// as success, so a second run is quiet.
func ReconcileLoginPolicies(ctx context.Context, inst InstancePolicies, orgs OrgPolicies, intent PolicyIntent, logf func(format string, args ...any)) error {
	if intent.SelfRegistration && !intent.MailCapable {
		logf("WARNING: self-registration was requested but registration stays closed: new accounts must verify their email, and no email can be sent. Set SMTP_HOST (Helm: smtp.host) to enable it")
	}
	smsActive, err := anySMSProviderActive(ctx, inst)
	if err != nil {
		return err
	}
	want := desiredPolicy{
		allowRegister:     intent.MailCapable && intent.SelfRegistration,
		hidePasswordReset: !intent.MailCapable,
		otpEmail:          intent.MailCapable,
		dropSMS:           !smsActive,
	}

	if err := reconcileOne(ctx, "instance", instanceTarget{inst}, want, logf); err != nil {
		return err
	}
	ids, err := listOrgIDs(ctx, inst)
	if err != nil {
		return err
	}
	for _, id := range ids {
		orgCtx := middleware.SetOrgID(ctx, id)
		getCtx, cancel := context.WithTimeout(orgCtx, callTimeout)
		resp, err := orgs.GetLoginPolicy(getCtx, &management.GetLoginPolicyRequest{})
		cancel()
		if err != nil {
			return fmt.Errorf("reading the login policy of org %s: %w", id, err)
		}
		if resp.GetIsDefault() {
			continue // the org inherits the instance policy reconciled above
		}
		if err := reconcileOne(orgCtx, "org "+id, orgTarget{orgs}, want, logf); err != nil {
			return err
		}
	}
	return nil
}

type desiredPolicy struct {
	allowRegister     bool
	hidePasswordReset bool
	otpEmail          bool
	dropSMS           bool
}

// policyTarget hides the admin/management split: the instance and org calls take different
// request types but mean the same thing.
type policyTarget interface {
	get(ctx context.Context) (*policy.LoginPolicy, error)
	update(ctx context.Context, p *policy.LoginPolicy) error
	addFactor(ctx context.Context, t policy.SecondFactorType) error
	removeFactor(ctx context.Context, t policy.SecondFactorType) error
}

func reconcileOne(ctx context.Context, name string, target policyTarget, want desiredPolicy, logf func(string, ...any)) error {
	getCtx, cancel := context.WithTimeout(ctx, callTimeout)
	current, err := target.get(getCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("reading the %s login policy: %w", name, err)
	}

	if current.GetAllowRegister() != want.allowRegister || current.GetHidePasswordReset() != want.hidePasswordReset {
		updated := proto.Clone(current).(*policy.LoginPolicy)
		updated.AllowRegister = want.allowRegister
		updated.HidePasswordReset = want.hidePasswordReset
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		err := target.update(callCtx, updated)
		cancel()
		if err != nil && !isNoOp(err) {
			return fmt.Errorf("updating the %s login policy: %w", name, err)
		}
		logf("%s login policy: allowRegister=%t hidePasswordReset=%t", name, want.allowRegister, want.hidePasswordReset)
	}

	factors := current.GetSecondFactors()
	switch {
	case want.otpEmail && !slices.Contains(factors, policy.SecondFactorType_SECOND_FACTOR_TYPE_OTP_EMAIL) && len(factors) > 0:
		// Only where a second factor is already allowed: a lone email factor would become the
		// policy's only option.
		if err := changeFactor(ctx, target.addFactor, policy.SecondFactorType_SECOND_FACTOR_TYPE_OTP_EMAIL); err != nil {
			return fmt.Errorf("adding email codes to the %s login policy: %w", name, err)
		}
		logf("%s login policy: email codes allowed", name)
	case !want.otpEmail && slices.Contains(factors, policy.SecondFactorType_SECOND_FACTOR_TYPE_OTP_EMAIL):
		if err := changeFactor(ctx, target.removeFactor, policy.SecondFactorType_SECOND_FACTOR_TYPE_OTP_EMAIL); err != nil {
			return fmt.Errorf("removing email codes from the %s login policy: %w", name, err)
		}
		logf("%s login policy: email codes removed (no mail delivery)", name)
	}
	if want.dropSMS && slices.Contains(factors, policy.SecondFactorType_SECOND_FACTOR_TYPE_OTP_SMS) {
		if err := changeFactor(ctx, target.removeFactor, policy.SecondFactorType_SECOND_FACTOR_TYPE_OTP_SMS); err != nil {
			return fmt.Errorf("removing SMS codes from the %s login policy: %w", name, err)
		}
		logf("%s login policy: SMS codes removed (no active SMS provider)", name)
	}
	return nil
}

func changeFactor(ctx context.Context, op func(context.Context, policy.SecondFactorType) error, t policy.SecondFactorType) error {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if err := op(callCtx, t); err != nil && !isNoOp(err) {
		return err
	}
	return nil
}

// isNoOp reports Zitadel's answers for a write that would change nothing.
func isNoOp(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "NotChanged") || strings.Contains(msg, "AlreadyExists") || strings.Contains(msg, "NotExisting")
}

func anySMSProviderActive(ctx context.Context, inst InstancePolicies) (bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := inst.ListSMSProviders(callCtx, &admin.ListSMSProvidersRequest{})
	if err != nil {
		return false, fmt.Errorf("listing SMS providers: %w", err)
	}
	for _, p := range resp.GetResult() {
		if p.GetState() == settings.SMSProviderConfigState_SMS_PROVIDER_CONFIG_ACTIVE {
			return true, nil
		}
	}
	return false, nil
}

func listOrgIDs(ctx context.Context, inst InstancePolicies) ([]string, error) {
	var ids []string
	for offset := uint64(0); ; {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		resp, err := inst.ListOrgs(callCtx, &admin.ListOrgsRequest{Query: &object.ListQuery{Offset: offset, Limit: orgPageSize}})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("listing orgs: %w", err)
		}
		for _, o := range resp.GetResult() {
			ids = append(ids, o.GetId())
		}
		offset += uint64(len(resp.GetResult()))
		if len(resp.GetResult()) == 0 || offset >= resp.GetDetails().GetTotalResult() {
			return ids, nil
		}
	}
}

// copyLoginPolicy fills an update request from a read policy by field name. The update calls
// replace the whole policy (there is no field mask), so a field missed here would be reset; a
// request field with no counterpart in the policy is an error rather than a silent reset.
func copyLoginPolicy(dst proto.Message, src *policy.LoginPolicy) error {
	d := dst.ProtoReflect()
	s := src.ProtoReflect()
	fields := d.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		sfd := s.Descriptor().Fields().ByName(fd.Name())
		if sfd == nil {
			return fmt.Errorf("copying the login policy: %s.%s has no counterpart in policy.LoginPolicy", d.Descriptor().Name(), fd.Name())
		}
		if s.Has(sfd) {
			d.Set(fd, s.Get(sfd))
		}
	}
	return nil
}

type instanceTarget struct{ api InstancePolicies }

func (t instanceTarget) get(ctx context.Context) (*policy.LoginPolicy, error) {
	resp, err := t.api.GetLoginPolicy(ctx, &admin.GetLoginPolicyRequest{})
	return resp.GetPolicy(), err
}

func (t instanceTarget) update(ctx context.Context, p *policy.LoginPolicy) error {
	req := &admin.UpdateLoginPolicyRequest{}
	if err := copyLoginPolicy(req, p); err != nil {
		return err
	}
	_, err := t.api.UpdateLoginPolicy(ctx, req)
	return err
}

func (t instanceTarget) addFactor(ctx context.Context, f policy.SecondFactorType) error {
	_, err := t.api.AddSecondFactorToLoginPolicy(ctx, &admin.AddSecondFactorToLoginPolicyRequest{Type: f})
	return err
}

func (t instanceTarget) removeFactor(ctx context.Context, f policy.SecondFactorType) error {
	_, err := t.api.RemoveSecondFactorFromLoginPolicy(ctx, &admin.RemoveSecondFactorFromLoginPolicyRequest{Type: f})
	return err
}

type orgTarget struct{ api OrgPolicies }

func (t orgTarget) get(ctx context.Context) (*policy.LoginPolicy, error) {
	resp, err := t.api.GetLoginPolicy(ctx, &management.GetLoginPolicyRequest{})
	return resp.GetPolicy(), err
}

func (t orgTarget) update(ctx context.Context, p *policy.LoginPolicy) error {
	req := &management.UpdateCustomLoginPolicyRequest{}
	if err := copyLoginPolicy(req, p); err != nil {
		return err
	}
	_, err := t.api.UpdateCustomLoginPolicy(ctx, req)
	return err
}

func (t orgTarget) addFactor(ctx context.Context, f policy.SecondFactorType) error {
	_, err := t.api.AddSecondFactorToLoginPolicy(ctx, &management.AddSecondFactorToLoginPolicyRequest{Type: f})
	return err
}

func (t orgTarget) removeFactor(ctx context.Context, f policy.SecondFactorType) error {
	_, err := t.api.RemoveSecondFactorFromLoginPolicy(ctx, &management.RemoveSecondFactorFromLoginPolicyRequest{Type: f})
	return err
}
