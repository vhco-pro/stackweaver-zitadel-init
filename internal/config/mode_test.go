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
		allow      bool // STACKWEAVER_ALLOW_RETURN_CODE=true (#866 D4)
		kubernetes bool
		want       NotificationMode
		wantErr    bool
		wantOptIn  bool // the error must also name the opt-in variable
	}{
		{raw: "", want: ModeEmail},
		{raw: "  ", want: ModeEmail},
		{raw: "email", want: ModeEmail},
		{raw: "email", kubernetes: true, want: ModeEmail},
		{raw: "", allow: true, want: ModeEmail},
		{raw: "return_code", allow: true, want: ModeReturnCode},
		{raw: "return_code", wantErr: true, wantOptIn: true},
		{raw: "return_code", kubernetes: true, wantErr: true},
		{raw: "return_code", allow: true, kubernetes: true, wantErr: true},
		{raw: "e-mail", wantErr: true},
		{raw: "Email", kubernetes: true, wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseNotificationMode(tt.raw, tt.allow, tt.kubernetes)
		if tt.wantErr {
			switch {
			case err == nil:
				t.Errorf("ParseNotificationMode(%q, allow=%v, k8s=%v) = %q, want an error", tt.raw, tt.allow, tt.kubernetes, got)
			case !strings.Contains(err.Error(), "STACKWEAVER_NOTIFICATION_MODE") || !strings.Contains(err.Error(), `"`+tt.raw+`"`):
				t.Errorf("error must name the variable and quote %q: %v", tt.raw, err)
			case tt.wantOptIn && !strings.Contains(err.Error(), AllowReturnCodeVar):
				t.Errorf("error must name the opt-in variable %s: %v", AllowReturnCodeVar, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ParseNotificationMode(%q, allow=%v, k8s=%v) = %q, %v; want %q", tt.raw, tt.allow, tt.kubernetes, got, err, tt.want)
		}
	}
}
