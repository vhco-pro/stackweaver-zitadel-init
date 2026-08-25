// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package k8s

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient points a Client at a httptest server with a retry schedule fast
// enough for unit tests (the production defaults are 45 s / 500 ms).
func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{
		http:        srv.Client(),
		token:       "test-token",
		apiBase:     srv.URL,
		retryBudget: 300 * time.Millisecond,
		retryBase:   5 * time.Millisecond,
	}
}

func TestGetSecretFound(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/v1/namespaces/sw/secrets/creds"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"password":"`+
			base64.StdEncoding.EncodeToString([]byte("s3cret"))+`"}}`)
	}))

	secret, found, err := client.GetSecret("creds", "sw")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if got := string(secret.Data["password"]); got != "s3cret" {
		t.Errorf("password = %q, want %q", got, "s3cret")
	}
}

func TestGetSecretNotFound(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"kind":"Status","code":404}`)
	}))

	secret, found, err := client.GetSecret("creds", "sw")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if found {
		t.Error("found = true, want false")
	}
	if secret != nil {
		t.Errorf("secret = %+v, want nil", secret)
	}
}

// A 401 is a definitive "no" from the API server and must surface as an error,
// never as "absent" - that distinction is the whole point of the tri-state.
func TestGetSecretUnauthorizedIsError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Unauthorized"}`)
	}))

	if _, found, err := client.GetSecret("creds", "sw"); err == nil {
		t.Fatalf("GetSecret err = nil, found = %v; want an error", found)
	}
}

func TestGetSecretMalformedJSONIsError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data": [not json`)
	}))

	if _, _, err := client.GetSecret("creds", "sw"); err == nil {
		t.Fatal("GetSecret err = nil, want a decode error")
	}
}

func TestGetSecretUndecodableValueIsError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"password":"!!!not-base64!!!"}}`)
	}))

	if _, _, err := client.GetSecret("creds", "sw"); err == nil {
		t.Fatal("GetSecret err = nil, want a base64 error")
	}
}

func TestForbiddenIsRetriedThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"reason":"Forbidden"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	_, found, err := client.GetSecret("creds", "sw")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if found {
		t.Error("found = true, want false")
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("request count = %d, want 3", got)
	}
}

func TestPersistentServerErrorExhaustsBudget(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	_, _, err := client.GetSecret("creds", "sw")
	if err == nil {
		t.Fatal("GetSecret err = nil, want a budget-exhausted error")
	}
	if !strings.Contains(err.Error(), "giving up after") {
		t.Errorf("err = %v, want a giving-up error", err)
	}
	if got := calls.Load(); got < 2 {
		t.Errorf("request count = %d, want at least 2 attempts", got)
	}
}

func TestReadAllSecretKeysMissingSecretIsEmpty(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	all, err := client.ReadAllSecretKeys("creds", "sw")
	if err != nil {
		t.Fatalf("ReadAllSecretKeys: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("map = %v, want empty", all)
	}
}

func TestReadAllSecretKeysTrimsAndPropagatesErrors(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"pat":"`+
			base64.StdEncoding.EncodeToString([]byte("  token\n"))+`"}}`)
	}))
	all, err := client.ReadAllSecretKeys("creds", "sw")
	if err != nil {
		t.Fatalf("ReadAllSecretKeys: %v", err)
	}
	if all["pat"] != "token" {
		t.Errorf("pat = %q, want %q", all["pat"], "token")
	}

	failing := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	if _, err := failing.ReadAllSecretKeys("creds", "sw"); err == nil {
		t.Fatal("ReadAllSecretKeys err = nil on 401, want an error")
	}
}
