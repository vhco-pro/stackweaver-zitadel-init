// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"testing"

	"github.com/michielvha/stackweaver/scripts/zitadel-init/internal/config"
)

// #829 D1: mail capability is one fact. Codes are deliverable when Zitadel has an active email
// provider, or when the (development-only) return_code mode shows them on screen.
func TestMailCapable(t *testing.T) {
	tests := []struct {
		activeSMTP bool
		mode       config.NotificationMode
		want       bool
	}{
		{activeSMTP: false, mode: config.ModeEmail, want: false},
		{activeSMTP: true, mode: config.ModeEmail, want: true},
		{activeSMTP: false, mode: config.ModeReturnCode, want: true},
		{activeSMTP: true, mode: config.ModeReturnCode, want: true},
	}
	for _, tt := range tests {
		if got := MailCapable(tt.activeSMTP, tt.mode); got != tt.want {
			t.Errorf("MailCapable(%v, %q) = %v, want %v", tt.activeSMTP, tt.mode, got, tt.want)
		}
	}
}
