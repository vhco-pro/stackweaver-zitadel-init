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

// AllowReturnCodeVar is the development-only opt-in without which return_code is refused. Same
// name and meaning as the API's.
const AllowReturnCodeVar = "STACKWEAVER_ALLOW_RETURN_CODE"

// AllowReturnCode reports whether the opt-in is set. Only the exact value "true" counts.
func AllowReturnCode(raw string) bool {
	return strings.TrimSpace(raw) == "true"
}

// ParseNotificationMode accepts email, return_code, or unset (email). return_code is refused in
// Kubernetes, where the API always runs with GIN_MODE=release and would refuse it as well, and
// elsewhere it needs the explicit opt-in (allowReturnCode), exactly as the API requires, so the
// two never disagree about whether codes can be returned.
func ParseNotificationMode(raw string, allowReturnCode, kubernetes bool) (NotificationMode, error) {
	switch strings.TrimSpace(raw) {
	case "", string(ModeEmail):
		return ModeEmail, nil
	case string(ModeReturnCode):
		if kubernetes {
			return "", fmt.Errorf("STACKWEAVER_NOTIFICATION_MODE %q is not allowed in Kubernetes: the API refuses it under GIN_MODE=release; use \"email\"", raw)
		}
		if !allowReturnCode {
			return "", fmt.Errorf("STACKWEAVER_NOTIFICATION_MODE %q needs %s=true, a development-only switch: the API refuses it without that; on anything but a development machine use \"email\"", raw, AllowReturnCodeVar)
		}
		return ModeReturnCode, nil
	default:
		return "", fmt.Errorf("STACKWEAVER_NOTIFICATION_MODE %q is not a valid mode: use \"email\" or, on a development machine only, \"return_code\"", raw)
	}
}
