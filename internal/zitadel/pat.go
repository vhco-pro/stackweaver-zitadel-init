// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	objectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WaitForPAT waits for a PAT file to appear at the given path with the default timeout.
func WaitForPAT(patPath string) (string, error) {
	return WaitForPATWithTimeout(patPath, maxWait)
}

// WaitForPATWithTimeout waits for a PAT file to appear at the given path.
func WaitForPATWithTimeout(patPath string, timeout time.Duration) (string, error) {
	fmt.Printf("⏳ Waiting for PAT file at %s (timeout %v)...\n", patPath, timeout)

	start := time.Now()
	for time.Since(start) < timeout {
		data, err := os.ReadFile(patPath)
		if err == nil && len(data) > 0 {
			pat := strings.TrimSpace(string(data))
			if pat != "" {
				fmt.Println("✅ PAT file found!")
				return pat, nil
			}
		}

		if time.Since(start).Seconds() > 0 && int(time.Since(start).Seconds())%10 == 0 {
			fmt.Printf("   Waiting... (%ds)\n", int(time.Since(start).Seconds()))
		}

		time.Sleep(waitInterval)
	}

	return "", fmt.Errorf("PAT file not found after %v", timeout)
}

// ValidateStoredPAT checks whether a PAT from the K8s secret is still valid
// against the live Zitadel instance. Returns false only on a definitive auth
// rejection (Unauthenticated / PermissionDenied) so the caller can fall through
// to the PAT file wait. Network errors or timeouts are treated as "assume valid"
// to avoid breaking normal pod restarts on a slow cluster.
func ValidateStoredPAT(accessToken, dialAddr, domain string) bool {
	c, err := NewClient(accessToken, dialAddr, domain)
	if err != nil {
		fmt.Printf("⚠️  Could not build client to validate stored PAT (%v), assuming valid\n", err)
		return true
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	_, err = c.userService.ListUsers(ctx, &userV2.ListUsersRequest{
		Query: &objectV2.ListQuery{Limit: 1},
	})
	if err != nil {
		code := status.Code(err)
		if code == codes.Unauthenticated || code == codes.PermissionDenied {
			fmt.Printf("⚠️  Stored admin PAT is no longer valid (gRPC %s), falling through to PAT file\n", code)
			return false
		}
		fmt.Printf("⚠️  Could not validate stored PAT (%v), assuming valid\n", err)
	}
	return true
}
