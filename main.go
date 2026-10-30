// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/config"
	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/k8s"
	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/output"
	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/zitadel"
)

func main() {
	// The Helm reconcile Job (#829 D9) applies only the identity-mail settings and exits.
	if reconcileOnly(os.Getenv) {
		runReconcileOnly()
		return
	}
	// The Compose one-shot that registers the URLs naming the API once it is healthy (apiurls.go).
	if apiURLsOnly(os.Getenv) {
		runAPIURLs()
		return
	}

	patPath := os.Getenv("ZITADEL_PAT_PATH")
	if patPath == "" {
		patPath = "/pat/admin.pat"
	}

	projectRoot := os.Getenv("PROJECT_ROOT")
	if projectRoot == "" {
		projectRoot = "/config"
	}

	fmt.Println("==========================================")
	fmt.Println("Zitadel Initialization (v2 gRPC)")
	fmt.Println("==========================================")
	fmt.Println()

	internalAddr := os.Getenv("ZITADEL_INTERNAL_ADDR")
	if internalAddr == "" {
		internalAddr = "internal-zitadel:8080"
	}

	// ZITADEL_DOMAIN must match Zitadel's ExternalDomain so the gRPC :authority
	// header resolves to the correct instance. Defaults to "localhost" for Docker Compose.
	domain := os.Getenv("ZITADEL_DOMAIN")

	// Acquire an admin PAT. In Kubernetes the sidecar shares an emptyDir with
	// the main Zitadel container. Zitadel writes the PAT file only during
	// FirstInstance (fresh DB). On pod restarts with an existing DB, the
	// emptyDir is empty and the PAT file never appears. To handle this:
	//
	//   1. ZITADEL_PAT env var (highest priority, always works)
	//   2. K8s Secret (fastest, check before waiting for file)
	//   3. PAT file from emptyDir (first boot, Zitadel writes it)
	//
	// After a successful file-based read, the PAT is also persisted into the
	// K8s Secret so future pod restarts can use source (2).
	var accessToken string
	var kc *k8s.Client   // lazily created, reused later for secret writes
	var patFromFile bool // track source so we know whether to store it

	if patFromEnv := os.Getenv("ZITADEL_PAT"); patFromEnv != "" {
		accessToken = patFromEnv
		fmt.Println("✅ Using PAT from environment variable")
	} else if k8s.IsRunning() {
		// K8s sidecar mode: wait for Zitadel first.
		if err := zitadel.WaitForReady(internalAddr); err != nil {
			fmt.Printf("❌ Zitadel not ready: %v\n", err)
			os.Exit(1)
		}

		// Check K8s Secret FIRST, on pod restarts with existing DB, this is
		// instant and avoids wasting 30s waiting for a PAT file that will never
		// appear (Zitadel only writes it during FirstInstance).
		var k8sErr error
		kc, k8sErr = k8s.NewClient()
		if k8sErr != nil {
			fmt.Printf("❌ Failed to create K8s client: %v\n", k8sErr)
			os.Exit(1)
		}
		secretName := os.Getenv("K8S_SECRET_NAME")
		namespace := os.Getenv("K8S_NAMESPACE")
		if namespace == "" {
			if b, readErr := os.ReadFile(k8s.NamespacePath); readErr == nil {
				namespace = strings.TrimSpace(string(b))
			}
		}
		adminPATKey := config.EnvOrDefault("K8S_KEY_ADMIN_PAT", "admin-pat")
		storedPAT, _ := kc.ReadSecretKey(secretName, namespace, adminPATKey)

		if storedPAT != "" {
			fmt.Println("ℹ️  Validating stored admin PAT...")
			if zitadel.ValidateStoredPAT(storedPAT, internalAddr, domain) {
				accessToken = storedPAT
				fmt.Println("✅ Using admin PAT from K8s Secret")
			} else {
				// Stored PAT is stale, the Zitadel DB was wiped (e.g. PVC deleted)
				// but the secret was kept. Fall through to the PAT file written by
				// Zitadel's FirstInstance bootstrap on the fresh DB.
				fmt.Println("ℹ️  Stored PAT is stale, waiting for PAT file (fresh DB)...")
				pat, err := zitadel.WaitForPATWithTimeout(patPath, 5*time.Minute)
				if err != nil {
					fmt.Println("❌ Stored PAT was stale and no PAT file appeared.")
					fmt.Println("   The Zitadel DB was likely wiped but the secret was not deleted.")
					fmt.Println("   To recover: delete the Zitadel secret, then reinstall.")
					os.Exit(1)
				}
				accessToken = pat
				patFromFile = true
				fmt.Println("✅ Using PAT from file (fresh DB after wipe)")
			}
		} else {
			// No PAT in secret, this is either a first install (PAT file will
			// appear during FirstInstance) or the secret was never populated.
			// Wait for the PAT file with a generous timeout.
			fmt.Println("ℹ️  No admin PAT in K8s Secret, waiting for PAT file (first boot)...")
			pat, err := zitadel.WaitForPATWithTimeout(patPath, 5*time.Minute)
			if err != nil {
				fmt.Println("❌ No admin PAT found, neither in K8s Secret nor PAT file")
				fmt.Println("   This usually means the first install did not complete successfully.")
				fmt.Println("   To recover: delete both the Zitadel PostgreSQL PVC and the Zitadel")
				fmt.Println("   secret, then reinstall to allow a clean first-time initialization.")
				os.Exit(1)
			}
			accessToken = pat
			patFromFile = true
			fmt.Println("✅ Using PAT from file (first boot)")
		}
	} else {
		// Docker Compose mode: wait the full duration for the PAT file.
		pat, err := zitadel.WaitForPAT(patPath)
		if err != nil {
			fmt.Printf("❌ PAT not found: %v\n", err)
			fmt.Println()
			fmt.Println("Please provide ZITADEL_PAT environment variable or ensure PAT file exists at:", patPath)
			os.Exit(1)
		}
		accessToken = pat
		patFromFile = true
		fmt.Println("✅ Using PAT from file")
	}

	// Wait for Zitadel gRPC to be ready (may already be done in K8s path above).
	if !k8s.IsRunning() {
		if err := zitadel.WaitForReady(internalAddr); err != nil {
			fmt.Printf("❌ Zitadel not ready: %v\n", err)
			os.Exit(1)
		}
	}

	// Create client - domain sets the gRPC :authority header to match Zitadel's ExternalDomain
	client, err := zitadel.NewClient(accessToken, internalAddr, domain)
	if err != nil {
		fmt.Printf("❌ Failed to create Zitadel client: %v\n", err)
		os.Exit(1)
	}

	// Create or get organization
	orgID, err := client.GetOrCreateOrg("IAC Platform")
	if err != nil {
		fmt.Printf("❌ Failed to get/create org: %v\n", err)
		os.Exit(1)
	}

	// Create or get project
	projectID, err := client.GetOrCreateProject(orgID, "IAC Platform Project")
	if err != nil {
		fmt.Printf("❌ Failed to get/create project: %v\n", err)
		os.Exit(1)
	}

	// Load extra redirect URIs from zitadel-init.yaml config
	initCfg := config.LoadZitadelInitConfig(projectRoot)
	var extraRedirectURIs, extraPostLogoutURIs []string
	if initCfg != nil {
		extraRedirectURIs = initCfg.FrontendRedirectURIs
		extraPostLogoutURIs = initCfg.FrontendPostLogoutRedirectURIs
	}

	// Also read redirect URIs from environment variables (used by the Helm chart
	// to inject the production app URL without requiring a mounted config file).
	if v := os.Getenv("FRONTEND_REDIRECT_URI"); v != "" {
		extraRedirectURIs = append(extraRedirectURIs, v)
	}
	if v := os.Getenv("FRONTEND_POST_LOGOUT_URI"); v != "" {
		extraPostLogoutURIs = append(extraPostLogoutURIs, v)
	}

	// The settings that name the API. On a fresh Compose install the api host does not resolve
	// yet, so they wait for the api-urls run (see apiurls.go).
	apiURLSettings := loadAPIURLs(os.Getenv)
	bffBackChannelURI, actionsOptions := apiURLSettings.firstRun(k8s.IsRunning(), internalAddr, lookupHost)
	if bffBackChannelURI != apiURLSettings.backChannelLogout || actionsOptions.PendingBase != "" {
		fmt.Println("ℹ️  The api host does not resolve yet (a fresh install): the BFF back-channel logout URI and the Actions")
		fmt.Println("   target addresses are registered by the api-urls run (zitadel-api-urls) once the API is healthy")
	}

	// In Compose every value is recorded in the env file the moment Zitadel returns it, so a run
	// that stops later keeps the secrets Zitadel hands out only once. Kubernetes writes its Secret
	// at the end, as before.
	envPath := output.ComposeEnvPath(projectRoot, os.Getenv)
	record := func(values [][2]string) error {
		if k8s.IsRunning() {
			return nil
		}
		return output.UpdateComposeEnv(envPath, values)
	}

	var (
		apiClientID, apiClientSecret   string
		bff                            output.BFFClient
		loginUserID, loginServiceToken string
		idpSyncKey, complementTokenKey string
	)
	steps := []provisionStep{
		{"API app", func() ([][2]string, error) {
			var err error
			apiClientID, apiClientSecret, err = client.GetOrCreateAPIApp(orgID, projectID)
			return [][2]string{{"ZITADEL_API_CLIENT_ID", apiClientID}, {"ZITADEL_API_CLIENT_SECRET", apiClientSecret}}, err
		}},
		// The confidential BFF app the API uses to complete browser sign-in. Zitadel returns its
		// secret only at creation, so it is regenerated when none is stored yet.
		{"BFF app", func() ([][2]string, error) {
			var bffHasSecret bool
			if k8s.IsRunning() {
				if kc == nil {
					var k8sErr error
					if kc, k8sErr = k8s.NewClient(); k8sErr != nil {
						return nil, fmt.Errorf("creating the Kubernetes client: %w", k8sErr)
					}
				}
				has, err := output.HasStoredKubernetesBFFSecret(kc)
				if err != nil {
					return nil, fmt.Errorf("reading the stored BFF client secret: %w", err)
				}
				bffHasSecret = has
			} else {
				bffHasSecret = output.HasStoredComposeBFFSecret(envPath)
			}
			// The localhost origin is for the Compose installs, which run on it; a deployment that
			// names its own redirect URI (the Helm chart) does not get it.
			redirectURIs, postLogoutURIs := zitadel.DefaultRedirectURIs(extraRedirectURIs, extraPostLogoutURIs, os.Getenv("FRONTEND_REDIRECT_URI") == "")
			id, secret, err := client.GetOrCreateBFFApp(projectID, redirectURIs, postLogoutURIs, bffBackChannelURI, bffHasSecret)
			bff = output.BFFClient{ClientID: id, ClientSecret: secret}
			return [][2]string{{"ZITADEL_BFF_CLIENT_ID", id}, {"ZITADEL_BFF_CLIENT_SECRET", secret}}, err
		}},
		// Browser sign-in goes through the BFF app only. An install from before it has a public
		// frontend app, which is retired: left active, a script on the SPA's origin could start its
		// own code flow with it and hold tokens in the page again. A fresh install never creates one.
		{"public frontend app", func() ([][2]string, error) {
			return nil, client.DeactivatePublicFrontendApp(projectID)
		}},
		// The login service user and its PAT for the Login v2 UI.
		{"login service user", func() ([][2]string, error) {
			var err error
			loginUserID, loginServiceToken, err = client.EnsureLoginServiceUser()
			if err == nil {
				fmt.Printf("✅ Login service user ready: %s\n", loginUserID)
			}
			return [][2]string{{"ZITADEL_LOGIN_SERVICE_USER_ID", loginUserID}, {"ZITADEL_LOGIN_SERVICE_USER_TOKEN", loginServiceToken}}, err
		}},
		// The Login V2 BaseURI points at the Stackweaver SPA /login routes. The ConfigMap's
		// DefaultInstance.Features.LoginV2.BaseURI in zitadel-defaults.yaml only applies on first
		// Zitadel init and uses the internal service name (not browser-reachable). This call updates
		// the database via the Feature API on every zitadel-init run, so the URL stays correct after
		// domain changes. Defaults to the local compose frontend at localhost:5173/login; Helm
		// overrides via LOGIN_UI_BASE_URL.
		{"Login V2 BaseURI", func() ([][2]string, error) {
			loginUIBaseURL := os.Getenv("LOGIN_UI_BASE_URL")
			if loginUIBaseURL == "" {
				loginUIBaseURL = "http://localhost:5173/login"
			}
			if err := client.EnsureLoginV2BaseURI(loginUIBaseURL); err != nil {
				fmt.Printf("⚠️  Warning: could not set Login V2 BaseURI: %v\n", err)
			}
			return nil, nil
		}},
		// Trusted + custom domains so Zitadel is reachable on both ExternalDomain and localhost.
		// Sources: env ZITADEL_CUSTOM_DOMAINS (comma-separated) or deploy/zitadel-init.yaml
		// (custom_domains). When ExternalDomain != localhost, "localhost" is automatically added so
		// internal services can still reach Zitadel without going through the external domain.
		{"trusted domains", func() ([][2]string, error) {
			domains := config.LoadCustomDomainsConfig(projectRoot)
			zitadelDefaults := config.LoadZitadelDefaults(projectRoot)
			if zitadelDefaults.ExternalDomain != "localhost" && zitadelDefaults.ExternalDomain != "" && !slices.Contains(domains, "localhost") {
				domains = append(domains, "localhost")
				fmt.Printf("ℹ️  ExternalDomain is %q, auto-adding localhost as custom domain for internal access\n", zitadelDefaults.ExternalDomain)
			}
			if len(domains) == 0 {
				fmt.Println("ℹ️  No custom domains configured (set custom_domains in zitadel-init.yaml or ZITADEL_CUSTOM_DOMAINS)")
				return nil, nil
			}
			fmt.Println()
			fmt.Println("--- Ensuring trusted domains ---")
			return nil, client.EnsureTrustedDomains(domains)
		}},
		// External identity providers (Azure AD, Generic OIDC), only when their variables are set.
		{"identity providers", func() ([][2]string, error) {
			return nil, client.ConfigureIdentityProviders()
		}},
		// Zitadel Actions V2 for SSO group claim passthrough: external webhooks instead of embedded
		// JavaScript (V1 doesn't work with Login V2), only when an external IdP is set up.
		{"Zitadel Actions", func() ([][2]string, error) {
			var err error
			idpSyncKey, complementTokenKey, err = client.ConfigureActions(actionsOptions)
			return [][2]string{{"ZITADEL_WEBHOOK_IDP_SYNC_KEY", idpSyncKey}, {"ZITADEL_WEBHOOK_COMPLEMENT_TOKEN_KEY", complementTokenKey}}, err
		}},
	}
	// Identity mail (#829): in Compose this container is separate from Zitadel, so a failure here
	// stops the run loudly. In Kubernetes the Helm reconcile Job owns these steps, because a crash
	// loop in this sidecar would take the whole Zitadel pod, and every login, down with it.
	if !k8s.IsRunning() {
		steps = append(steps,
			provisionStep{"identity mail", func() ([][2]string, error) {
				return nil, reconcileIdentityMail(context.Background(), client, false)
			}},
			provisionStep{"token lifetime", func() ([][2]string, error) {
				return nil, reconcileTokenLifetime(context.Background(), client)
			}},
		)
	}
	if err := runProvisionSteps(steps, record); err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	// Write credentials, K8s: patch the Secret directly; Docker Compose: write .env.
	if k8s.IsRunning() {
		if kc == nil {
			var k8sErr error
			kc, k8sErr = k8s.NewClient()
			if k8sErr != nil {
				fmt.Printf("❌ Failed to create Kubernetes client: %v\n", k8sErr)
				os.Exit(1)
			}
		}
		if err := output.WriteKubernetesSecret(kc, apiClientID, apiClientSecret, loginServiceToken, idpSyncKey, complementTokenKey, bff); err != nil {
			fmt.Printf("❌ Failed to patch Kubernetes secret: %v\n", err)
			os.Exit(1)
		}
		// Persist the admin PAT so future pod restarts can skip the file wait.
		// This is only needed when we read the PAT from the emptyDir file (first boot).
		if patFromFile {
			adminPATKey := config.EnvOrDefault("K8S_KEY_ADMIN_PAT", "admin-pat")
			secretName := os.Getenv("K8S_SECRET_NAME")
			namespace := os.Getenv("K8S_NAMESPACE")
			if namespace == "" {
				if b, readErr := os.ReadFile(k8s.NamespacePath); readErr == nil {
					namespace = strings.TrimSpace(string(b))
				}
			}
			if err := kc.PatchSecret(secretName, namespace, map[string]string{adminPATKey: accessToken}); err != nil {
				fmt.Printf("⚠️  Warning: could not store admin PAT in K8s Secret: %v\n", err)
			} else {
				fmt.Println("✅ Stored admin PAT in K8s Secret for future pod restarts")
			}
		}

		// Wait for the OIDC projection to be ready before restarting consumers.
		// Consumers must not restart until Zitadel can resolve the BFF client ID
		// (the one browser sign-in uses) on the /oauth/v2/authorize endpoint, otherwise the frontend gets
		// Errors.App.NotFound until the projection catches up (up to 20+ min on
		// a cold or resource-constrained cluster).
		// This runs after ALL setup so time spent on app/user creation counts
		// toward the projection warming up. 30 minutes covers the worst observed
		// lag (~23 min). On re-runs with an existing DB projections are already
		// populated and this returns within a few seconds.
		client.WaitForOIDCProjection(bff.ClientID, 30*time.Minute)

		fmt.Println()
		fmt.Println("--- Restarting Zitadel consumers ---")
		k8s.RestartConsumers(kc)
	} else {
		if err := output.WriteComposeEnv(envPath, projectRoot, apiClientID, apiClientSecret, loginServiceToken, loginUserID, idpSyncKey, complementTokenKey, bff); err != nil {
			fmt.Printf("❌ Failed to write config files: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Println()
	fmt.Println("==========================================")
	fmt.Println("✅ Zitadel initialization complete!")
	fmt.Println("==========================================")
	fmt.Println()
	fmt.Printf("BFF Client ID: %s\n", bff.ClientID)
	fmt.Printf("API Client ID: %s\n", apiClientID)
	fmt.Println()

	// In Kubernetes the sidecar must not exit (Deployment restartPolicy: Always
	// would cause a restart loop). Serve a health endpoint and block forever;
	// if the pod restarts due to an upgrade, the sidecar re-provisions idempotently.
	if k8s.IsRunning() {
		healthPort := os.Getenv("HEALTH_PORT")
		if healthPort == "" {
			healthPort = "8081"
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ok")
		})
		go func() {
			fmt.Printf("Health endpoint listening on :%s/healthz\n", healthPort)
			if err := http.ListenAndServe(":"+healthPort, mux); err != nil {
				fmt.Printf("⚠️  Health server error: %v\n", err)
			}
		}()
		fmt.Println("Provisioning complete, entering idle state (sidecar mode).")
		select {}
	}

	fmt.Println(output.ComposeClosingMessage())
}

// sync smoke test 1779753492
