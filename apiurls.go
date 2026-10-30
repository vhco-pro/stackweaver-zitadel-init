// Copyright (c) 2026 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/config"
	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/zitadel"
)

// Two Zitadel settings name the API: the BFF app's back-channel logout URI and the Actions
// targets. Zitadel resolves a URL's host when it saves it, and in Compose the api container starts
// only after zitadel-init has succeeded, so on a fresh install neither can be saved by the first
// run. The first run therefore leaves them out while the api host does not resolve, and the
// api-urls run (ZITADEL_INIT_MODE=api-urls, a one-shot Compose service that waits for the API to be
// healthy) adds them. On a stack whose API is already running, the first run saves them as before
// and the api-urls run only confirms them.

// apiURLsOnly reports whether this run is the Compose api-urls run.
func apiURLsOnly(getenv func(string) string) bool {
	return strings.TrimSpace(getenv("ZITADEL_INIT_MODE")) == "api-urls"
}

// apiURLsToken returns the admin PAT for the api-urls run: ZITADEL_PAT, or the file Zitadel's first
// instance wrote. The run starts after zitadel-init succeeded, so the file exists; there is no wait.
func apiURLsToken(getenv func(string) string, readFile func(string) ([]byte, error)) (string, error) {
	if pat := strings.TrimSpace(getenv("ZITADEL_PAT")); pat != "" {
		return pat, nil
	}
	path := getenv("ZITADEL_PAT_PATH")
	if path == "" {
		path = "/pat/admin.pat"
	}
	b, err := readFile(path)
	if err != nil {
		return "", fmt.Errorf("reading the admin PAT at %s: %w", path, err)
	}
	if pat := strings.TrimSpace(string(b)); pat != "" {
		return pat, nil
	}
	return "", fmt.Errorf("the admin PAT at %s is empty", path)
}

// apiURLs are the settings that point Zitadel at the API.
type apiURLs struct {
	backChannelLogout string // ZITADEL_BFF_BACKCHANNEL_LOGOUT_URI
	actionsBase       string // ACTIONS_API_BASE_URL
}

func loadAPIURLs(getenv func(string) string) apiURLs {
	return apiURLs{
		backChannelLogout: getenv("ZITADEL_BFF_BACKCHANNEL_LOGOUT_URI"),
		actionsBase:       getenv("ACTIONS_API_BASE_URL"),
	}
}

// pendingPath is where the first run parks new Actions targets until the api-urls run moves them:
// on Zitadel itself, which resolves because it is the server being called.
const pendingPath = "/stackweaver-api-pending"

// firstRun returns what the full provisioning run registers: the back-channel logout URI ("" to
// leave it for the api-urls run) and the Actions options. It defers only in Compose and only while
// a configured api host does not resolve, which is exactly when Zitadel would refuse to save it.
// Kubernetes never defers: a Service name resolves before its pods are ready.
func (u apiURLs) firstRun(kubernetes bool, internalAddr string, lookup func(host string) error) (string, zitadel.ActionsOptions) {
	actions := zitadel.ActionsOptions{BaseURL: u.actionsBase}
	if kubernetes || u.resolvable(lookup) {
		return u.backChannelLogout, actions
	}
	actions.PendingBase = "http://" + internalAddr + pendingPath
	return "", actions
}

// resolvable reports whether every configured URL's host resolves. A URL that does not parse, or
// names an IP address, is left for Zitadel to judge.
func (u apiURLs) resolvable(lookup func(host string) error) bool {
	for _, raw := range []string{u.backChannelLogout, u.actionsBase} {
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" || net.ParseIP(parsed.Hostname()) != nil {
			continue
		}
		if lookup(parsed.Hostname()) != nil {
			return false
		}
	}
	return true
}

func lookupHost(host string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := net.DefaultResolver.LookupHost(ctx, host)
	return err
}

// provisionStep is one step of the full run. run returns the values it produced (KEY, value) for
// the Compose env file; a secret Zitadel only hands out once is among them.
type provisionStep struct {
	name string
	run  func() ([][2]string, error)
}

// runProvisionSteps runs the steps in order and records each step's values before the next step
// starts, so a run that fails or is killed later keeps every secret Zitadel has already returned,
// and the rerun (for which Zitadel returns no secret for the objects that now exist) still has
// them. A failing step records nothing and ends the run.
func runProvisionSteps(steps []provisionStep, record func([][2]string) error) error {
	for _, s := range steps {
		values, err := s.run()
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		if len(values) == 0 {
			continue
		}
		if err := record(values); err != nil {
			return fmt.Errorf("recording the values of %s: %w", s.name, err)
		}
	}
	return nil
}

// runAPIURLs is the entry point of the Compose api-urls run.
func runAPIURLs() {
	fmt.Println("==========================================")
	fmt.Println("Zitadel API URLs (back-channel logout, Actions targets)")
	fmt.Println("==========================================")
	if err := registerAPIURLs(); err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ API URLs registered")
}

func registerAPIURLs() error {
	token, err := apiURLsToken(os.Getenv, os.ReadFile)
	if err != nil {
		return err
	}
	internalAddr := config.EnvOrDefault("ZITADEL_INTERNAL_ADDR", "internal-zitadel:8080")
	if err := zitadel.WaitForReady(internalAddr); err != nil {
		return fmt.Errorf("zitadel not ready: %w", err)
	}
	client, err := zitadel.NewClient(token, internalAddr, os.Getenv("ZITADEL_DOMAIN"))
	if err != nil {
		return fmt.Errorf("creating the Zitadel client: %w", err)
	}
	urls := loadAPIURLs(os.Getenv)
	if urls.backChannelLogout == "" && urls.actionsBase == "" {
		return errors.New("neither ZITADEL_BFF_BACKCHANNEL_LOGOUT_URI nor ACTIONS_API_BASE_URL is set; nothing to register")
	}

	orgID, err := client.GetOrCreateOrg("IAC Platform")
	if err != nil {
		return fmt.Errorf("finding the organization: %w", err)
	}
	projectID, err := client.GetOrCreateProject(orgID, "IAC Platform Project")
	if err != nil {
		return fmt.Errorf("finding the project: %w", err)
	}
	if urls.backChannelLogout != "" {
		fmt.Println()
		fmt.Printf("--- BFF back-channel logout URI: %s ---\n", urls.backChannelLogout)
		if err := client.EnsureBFFBackChannelLogout(projectID, urls.backChannelLogout); err != nil {
			return fmt.Errorf("registering the back-channel logout URI: %w", err)
		}
	}
	if _, _, err := client.ConfigureActions(zitadel.ActionsOptions{BaseURL: urls.actionsBase, RequireExisting: true}); err != nil {
		return fmt.Errorf("pointing the Actions targets at the API: %w", err)
	}
	return nil
}
