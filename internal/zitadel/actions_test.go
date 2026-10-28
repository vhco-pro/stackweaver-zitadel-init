// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"testing"

	actionV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/action/v2"
	"google.golang.org/grpc"
)

// fakeActions is an in-memory ActionService holding targets. It embeds the interface so the
// methods getOrCreateTarget does not call panic if they are reached.
type fakeActions struct {
	actionV2.ActionServiceClient
	targets []*actionV2.Target
	updates []*actionV2.UpdateTargetRequest
	created int
}

func (f *fakeActions) ListTargets(context.Context, *actionV2.ListTargetsRequest, ...grpc.CallOption) (*actionV2.ListTargetsResponse, error) {
	return &actionV2.ListTargetsResponse{Targets: f.targets}, nil
}

func (f *fakeActions) UpdateTarget(_ context.Context, in *actionV2.UpdateTargetRequest, _ ...grpc.CallOption) (*actionV2.UpdateTargetResponse, error) {
	f.updates = append(f.updates, in)
	for _, t := range f.targets {
		if t.GetId() == in.GetId() && in.Endpoint != nil {
			t.Endpoint = in.GetEndpoint()
		}
	}
	return &actionV2.UpdateTargetResponse{}, nil
}

func (f *fakeActions) CreateTarget(_ context.Context, in *actionV2.CreateTargetRequest, _ ...grpc.CallOption) (*actionV2.CreateTargetResponse, error) {
	f.created++
	f.targets = append(f.targets, &actionV2.Target{Id: "new", Name: in.GetName(), Endpoint: in.GetEndpoint()})
	return &actionV2.CreateTargetResponse{Id: "new", SigningKey: "key"}, nil
}

func TestGetOrCreateTarget_RepairsAStaleEndpointWithoutRotatingTheKey(t *testing.T) {
	f := &fakeActions{targets: []*actionV2.Target{
		{Id: "t1", Name: "stackweaver-complement-token", Endpoint: "http://localhost:8022/api/v2/zitadel/actions/complement-token"},
	}}
	c := &Client{ctx: context.Background(), actionService: f}

	id, key, err := c.getOrCreateTarget("stackweaver-complement-token", "http://api:8022/api/v2/zitadel/actions/complement-token", true)
	if err != nil {
		t.Fatal(err)
	}
	if id != "t1" || key != "" {
		t.Errorf("got id %q key %q, want the existing target and no new key", id, key)
	}
	if len(f.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(f.updates))
	}
	u := f.updates[0]
	if u.GetEndpoint() != "http://api:8022/api/v2/zitadel/actions/complement-token" {
		t.Errorf("updated endpoint = %q", u.GetEndpoint())
	}
	if u.ExpirationSigningKey != nil {
		t.Error("the update rotates the signing key; the API holds the current one in its environment")
	}
	if f.created != 0 {
		t.Error("an existing target was created again")
	}
}

func TestGetOrCreateTarget_LeavesACorrectTargetAlone(t *testing.T) {
	endpoint := "http://api:8022/api/v2/zitadel/actions/idp-sync"
	f := &fakeActions{targets: []*actionV2.Target{{Id: "t2", Name: "stackweaver-idp-sync", Endpoint: endpoint}}}
	c := &Client{ctx: context.Background(), actionService: f}

	if _, _, err := c.getOrCreateTarget("stackweaver-idp-sync", endpoint, true); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 0 || f.created != 0 {
		t.Errorf("updates = %d, created = %d; a target at the right endpoint needs neither", len(f.updates), f.created)
	}
}

func TestGetOrCreateTarget_KeepsAHandCorrectedTargetWithoutAConfiguredURL(t *testing.T) {
	// No ACTIONS_API_BASE_URL: the endpoint is the localhost fallback, and the target was
	// corrected in the Zitadel console. Moving it back would break SSO group delivery.
	f := &fakeActions{targets: []*actionV2.Target{
		{Id: "t3", Name: "stackweaver-complement-token", Endpoint: "http://api:8022/api/v2/zitadel/actions/complement-token"},
	}}
	c := &Client{ctx: context.Background(), actionService: f}

	if _, _, err := c.getOrCreateTarget("stackweaver-complement-token", "http://localhost:8022/api/v2/zitadel/actions/complement-token", false); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 0 {
		t.Errorf("updates = %d; a target is moved only to an explicitly configured endpoint", len(f.updates))
	}
}
