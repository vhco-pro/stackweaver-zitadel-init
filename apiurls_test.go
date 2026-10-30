// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/output"
)

func TestAPIURLsMode(t *testing.T) {
	if apiURLsOnly(envMap(map[string]string{})) {
		t.Error("the default run is the full provisioning flow")
	}
	if !apiURLsOnly(envMap(map[string]string{"ZITADEL_INIT_MODE": "api-urls"})) {
		t.Error("ZITADEL_INIT_MODE=api-urls selects the api-urls run")
	}
	if apiURLsOnly(envMap(map[string]string{"ZITADEL_INIT_MODE": "reconcile"})) || reconcileOnly(envMap(map[string]string{"ZITADEL_INIT_MODE": "api-urls"})) {
		t.Error("the Helm identity-mail reconcile and the Compose api-urls run are separate modes")
	}
}

// The api-urls run reads the admin PAT from the environment or, in Compose, from the file
// Zitadel's first instance wrote; it does not wait for one to appear.
func TestAPIURLsToken(t *testing.T) {
	read := func(path string) ([]byte, error) {
		if path == "/pat/admin.pat" {
			return []byte("pat-from-file\n"), nil
		}
		return nil, os.ErrNotExist
	}
	if tok, err := apiURLsToken(envMap(map[string]string{"ZITADEL_PAT": "pat-from-env"}), read); err != nil || tok != "pat-from-env" {
		t.Errorf("env PAT: %q, %v", tok, err)
	}
	if tok, err := apiURLsToken(envMap(map[string]string{"ZITADEL_PAT_PATH": "/pat/admin.pat"}), read); err != nil || tok != "pat-from-file" {
		t.Errorf("file PAT: %q, %v", tok, err)
	}
	if _, err := apiURLsToken(envMap(map[string]string{"ZITADEL_PAT_PATH": "/missing"}), read); err == nil {
		t.Error("a missing PAT must fail at once")
	}
}

func resolvesOnly(hosts ...string) func(string) error {
	return func(h string) error {
		for _, ok := range hosts {
			if h == ok {
				return nil
			}
		}
		return errors.New("no such host")
	}
}

var composeURLs = apiURLs{
	backChannelLogout: "http://api:8022/auth/oidc/end-session",
	actionsBase:       "http://api:8022",
}

// A fresh Compose install: api does not resolve, so the first run names neither URL and creates
// the targets at a pending address on Zitadel itself; the api-urls run adds the real ones.
func TestFirstRun_DefersTheAPIURLsWhileTheAPIHostDoesNotResolve(t *testing.T) {
	bff, actions := composeURLs.firstRun(false, "zitadel:8080", resolvesOnly("zitadel"))
	if bff != "" {
		t.Errorf("back-channel URI %q on a first run whose api host does not resolve", bff)
	}
	if actions.PendingBase == "" || strings.Contains(actions.PendingBase, "api:8022") {
		t.Errorf("pending base %q; want an address on Zitadel itself", actions.PendingBase)
	}
	if actions.BaseURL != composeURLs.actionsBase || actions.RequireExisting {
		t.Errorf("actions options %+v", actions)
	}
}

// A rerun on a stack whose api is up (the long-lived dev stack) registers the URLs directly, as it
// always has, so the api-urls run that follows only confirms them.
func TestFirstRun_RegistersTheAPIURLsWhenTheAPIHostResolves(t *testing.T) {
	bff, actions := composeURLs.firstRun(false, "zitadel:8080", resolvesOnly("zitadel", "api"))
	if bff != composeURLs.backChannelLogout || actions.PendingBase != "" {
		t.Errorf("got %q, %+v; want the URLs registered directly", bff, actions)
	}
}

// Kubernetes is unchanged: a Service name resolves before its pods are ready, and the Helm chart
// has no api-urls run to defer to.
func TestFirstRun_KubernetesNeverDefers(t *testing.T) {
	bff, actions := composeURLs.firstRun(true, "zitadel:8080", resolvesOnly())
	if bff != composeURLs.backChannelLogout || actions.PendingBase != "" {
		t.Errorf("got %q, %+v; want the URLs registered directly", bff, actions)
	}
}

// A first run that dies after Zitadel handed out its one-time secrets keeps them: each step's
// secrets are on disk before the next step starts, and the rerun, for which Zitadel returns no
// secret for the objects that now exist, completes without losing them.
func TestProvisionSteps_SecretsSurviveALaterFailureAndARerun(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "deploy", ".env")
	record := func(p [][2]string) error { return output.UpdateComposeEnv(envPath, p) }

	firstRun := []provisionStep{
		{"API app", func() ([][2]string, error) {
			return [][2]string{{"ZITADEL_API_CLIENT_ID", "api-id"}, {"ZITADEL_API_CLIENT_SECRET", "api-secret"}}, nil
		}},
		{"BFF app", func() ([][2]string, error) {
			return [][2]string{{"ZITADEL_BFF_CLIENT_ID", "bff-id"}, {"ZITADEL_BFF_CLIENT_SECRET", "bff-secret"}}, nil
		}},
		{"login service user", func() ([][2]string, error) {
			return [][2]string{{"ZITADEL_LOGIN_SERVICE_USER_ID", "login-id"}, {"ZITADEL_LOGIN_SERVICE_USER_TOKEN", "login-token"}}, nil
		}},
		{"actions", func() ([][2]string, error) { return nil, errors.New("injected failure after the secrets were created") }},
	}
	if err := runProvisionSteps(firstRun, record); err == nil || !strings.Contains(err.Error(), "actions") {
		t.Fatalf("want the injected failure, named by its step, got %v", err)
	}
	want := map[string]string{
		"ZITADEL_API_CLIENT_SECRET":        "api-secret",
		"ZITADEL_BFF_CLIENT_SECRET":        "bff-secret",
		"ZITADEL_LOGIN_SERVICE_USER_TOKEN": "login-token",
	}
	assertEnv(t, envPath, want)

	rerun := []provisionStep{
		{"API app", func() ([][2]string, error) {
			return [][2]string{{"ZITADEL_API_CLIENT_ID", "api-id"}, {"ZITADEL_API_CLIENT_SECRET", ""}}, nil
		}},
		{"BFF app", func() ([][2]string, error) {
			return [][2]string{{"ZITADEL_BFF_CLIENT_ID", "bff-id"}, {"ZITADEL_BFF_CLIENT_SECRET", ""}}, nil
		}},
		{"login service user", func() ([][2]string, error) {
			return [][2]string{{"ZITADEL_LOGIN_SERVICE_USER_ID", "login-id"}, {"ZITADEL_LOGIN_SERVICE_USER_TOKEN", ""}}, nil
		}},
		{"actions", func() ([][2]string, error) { return nil, nil }},
	}
	if err := runProvisionSteps(rerun, record); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	assertEnv(t, envPath, want)
}

// A step that fails records nothing of its own, and the steps after it do not run.
func TestProvisionSteps_StopAtTheFirstFailure(t *testing.T) {
	var ran []string
	var recorded [][2]string
	steps := []provisionStep{
		{"a", func() ([][2]string, error) { ran = append(ran, "a"); return [][2]string{{"A", "1"}}, nil }},
		{"b", func() ([][2]string, error) {
			ran = append(ran, "b")
			return [][2]string{{"B", "2"}}, errors.New("boom")
		}},
		{"c", func() ([][2]string, error) { ran = append(ran, "c"); return nil, nil }},
	}
	err := runProvisionSteps(steps, func(p [][2]string) error { recorded = append(recorded, p...); return nil })
	if err == nil || strings.Join(ran, "") != "ab" || len(recorded) != 1 || recorded[0][0] != "A" {
		t.Fatalf("err %v, ran %v, recorded %v", err, ran, recorded)
	}
}

func assertEnv(t *testing.T, path string, want map[string]string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
