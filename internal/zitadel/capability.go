// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import "github.com/michielvha/stackweaver/scripts/zitadel-init/internal/config"

// MailCapable reports whether auth codes can reach users (#829 D1): Zitadel has an active email
// provider, or the development-only return_code mode shows codes on screen. Every login-policy
// decision that depends on mail keys off this one value.
func MailCapable(activeSMTP bool, mode config.NotificationMode) bool {
	return activeSMTP || mode == config.ModeReturnCode
}
