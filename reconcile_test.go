// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package main

import (
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestReconcileOnlyMode(t *testing.T) {
	if reconcileOnly(envMap(map[string]string{})) {
		t.Error("the default run is the full provisioning flow")
	}
	if !reconcileOnly(envMap(map[string]string{"ZITADEL_INIT_MODE": "reconcile"})) {
		t.Error("ZITADEL_INIT_MODE=reconcile selects the reconcile-only mode")
	}
}

// #829 D9: the Helm reconcile Job gets the admin PAT through a secretKeyRef. It has no emptyDir,
// so it must fail immediately without ZITADEL_PAT instead of entering the 5-minute file wait.
func TestReconcileTokenNeverWaitsForThePATFile(t *testing.T) {
	tok, err := reconcileToken(envMap(map[string]string{"ZITADEL_PAT": "pat-from-secret"}))
	if err != nil || tok != "pat-from-secret" {
		t.Fatalf("reconcileToken = %q, %v; want the env PAT", tok, err)
	}
	_, err = reconcileToken(envMap(map[string]string{"ZITADEL_PAT_PATH": "/pat/admin.pat"}))
	if err == nil || !strings.Contains(err.Error(), "ZITADEL_PAT") {
		t.Fatalf("without ZITADEL_PAT the reconcile mode must fail fast naming it, got %v", err)
	}
}
