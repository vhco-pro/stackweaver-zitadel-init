// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/config"
	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/zitadel"
)

// reconcileOnly reports whether this run is the Helm reconcile Job (#829 D9) rather than the
// full provisioning flow. The Job applies only the identity-mail settings and exits.
func reconcileOnly(getenv func(string) string) bool {
	return strings.TrimSpace(getenv("ZITADEL_INIT_MODE")) == "reconcile"
}

// reconcileToken returns the admin PAT for the reconcile Job. The Job receives it through a
// secretKeyRef to the key the sidecar stores; it has no emptyDir, so unlike the sidecar it never
// waits for a PAT file.
func reconcileToken(getenv func(string) string) (string, error) {
	if pat := strings.TrimSpace(getenv("ZITADEL_PAT")); pat != "" {
		return pat, nil
	}
	return "", errors.New("ZITADEL_PAT is required in reconcile mode: the Job reads the admin PAT from the Zitadel Secret, which the sidecar writes after its first successful run")
}

func logf(format string, args ...any) {
	fmt.Printf("ℹ️  "+format+"\n", args...)
}

// reconcileIdentityMail applies the SMTP provider, derives mail capability and makes the login
// policy follow it (#829 D1, D2, D4), in that order.
// Every error is fatal to the caller: a skipped step would leave Zitadel's accidental open
// registration in place while the install reports success.
func reconcileIdentityMail(ctx context.Context, c *zitadel.Client, kubernetes bool) error {
	smtpCfg, err := config.LoadSMTP(os.Getenv)
	if err != nil {
		return err
	}
	mode, err := config.ParseNotificationMode(os.Getenv("STACKWEAVER_NOTIFICATION_MODE"), kubernetes)
	if err != nil {
		return err
	}
	selfRegistration, err := config.LoadSelfRegistration(os.Getenv)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("--- Reconciling identity mail ---")
	activeSMTP, err := zitadel.ReconcileSMTP(ctx, c.Admin(), smtpCfg, logf)
	if err != nil {
		return fmt.Errorf("reconciling SMTP: %w", err)
	}
	capable := zitadel.MailCapable(activeSMTP, mode)
	logf("mail capability: %t (active email provider: %t, notification mode: %s)", capable, activeSMTP, mode)

	intent := zitadel.PolicyIntent{MailCapable: capable, SelfRegistration: selfRegistration}
	if err := zitadel.ReconcileLoginPolicies(ctx, c.Admin(), c.Management(), intent, logf); err != nil {
		return fmt.Errorf("reconciling login policies: %w", err)
	}
	return nil
}

// runReconcileOnly is the entry point of the Helm reconcile Job.
func runReconcileOnly() {
	fmt.Println("==========================================")
	fmt.Println("Zitadel identity-mail reconcile")
	fmt.Println("==========================================")

	token, err := reconcileToken(os.Getenv)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	internalAddr := config.EnvOrDefault("ZITADEL_INTERNAL_ADDR", "internal-zitadel:8080")
	if err := zitadel.WaitForReady(internalAddr); err != nil {
		fmt.Printf("❌ Zitadel not ready: %v\n", err)
		os.Exit(1)
	}
	client, err := zitadel.NewClient(token, internalAddr, os.Getenv("ZITADEL_DOMAIN"))
	if err != nil {
		fmt.Printf("❌ Failed to create Zitadel client: %v\n", err)
		os.Exit(1)
	}
	if err := reconcileIdentityMail(context.Background(), client, true); err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ Identity-mail reconcile complete")
}
