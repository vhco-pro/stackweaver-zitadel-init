// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package config

import (
	"fmt"
	"strings"
)

// NotificationMode mirrors the API's STACKWEAVER_NOTIFICATION_MODE (#829 D1). zitadel-init lives
// in its own module outside go.work, so it keeps a copy of the parser with identical rules.
type NotificationMode string

const (
	ModeEmail      NotificationMode = "email"
	ModeReturnCode NotificationMode = "return_code"
)

// ParseNotificationMode accepts email, return_code, or unset (email). return_code is refused in
// Kubernetes, where the API always runs with GIN_MODE=release and would refuse it as well.
func ParseNotificationMode(raw string, kubernetes bool) (NotificationMode, error) {
	switch strings.TrimSpace(raw) {
	case "", string(ModeEmail):
		return ModeEmail, nil
	case string(ModeReturnCode):
		if kubernetes {
			return "", fmt.Errorf("STACKWEAVER_NOTIFICATION_MODE %q is not allowed in Kubernetes: the API refuses it under GIN_MODE=release; use \"email\"", raw)
		}
		return ModeReturnCode, nil
	default:
		return "", fmt.Errorf("STACKWEAVER_NOTIFICATION_MODE %q is not a valid mode: use \"email\" or, on a development machine only, \"return_code\"", raw)
	}
}
