// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package config

import (
	"strings"
	"testing"
)

// #829 D1: zitadel-init reads the same STACKWEAVER_NOTIFICATION_MODE values as the API, so the
// two cannot disagree about a typo. In Kubernetes the API runs with GIN_MODE=release and refuses
// return_code, so zitadel-init refuses it too rather than computing mail capability from it.
func TestParseNotificationMode(t *testing.T) {
	tests := []struct {
		raw        string
		kubernetes bool
		want       NotificationMode
		wantErr    bool
	}{
		{raw: "", want: ModeEmail},
		{raw: "  ", want: ModeEmail},
		{raw: "email", want: ModeEmail},
		{raw: "email", kubernetes: true, want: ModeEmail},
		{raw: "return_code", want: ModeReturnCode},
		{raw: "return_code", kubernetes: true, wantErr: true},
		{raw: "e-mail", wantErr: true},
		{raw: "Email", kubernetes: true, wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseNotificationMode(tt.raw, tt.kubernetes)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseNotificationMode(%q, k8s=%v) = %q, want an error", tt.raw, tt.kubernetes, got)
			} else if !strings.Contains(err.Error(), "STACKWEAVER_NOTIFICATION_MODE") || !strings.Contains(err.Error(), `"`+tt.raw+`"`) {
				t.Errorf("error must name the variable and quote %q: %v", tt.raw, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ParseNotificationMode(%q, k8s=%v) = %q, %v; want %q", tt.raw, tt.kubernetes, got, err, tt.want)
		}
	}
}
