// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SMTPConfig is the identity-mail provider zitadel-init configures in Zitadel (#829 D2).
// It comes from deploy/sso.env in Compose and from the Helm smtp.* values in Kubernetes.
type SMTPConfig struct {
	Host          string // host:port of the SMTP submission server; empty means no SMTP
	User          string // empty means the provider is used without authentication
	Password      string
	SenderAddress string
	SenderName    string
	TLS           bool
}

// Enabled reports whether an SMTP provider was configured at all.
func (c SMTPConfig) Enabled() bool { return c.Host != "" }

// String describes the configuration for logs without the password.
func (c SMTPConfig) String() string {
	if !c.Enabled() {
		return "no SMTP provider configured"
	}
	auth := "no authentication"
	if c.User != "" {
		auth = "user " + c.User
	}
	return fmt.Sprintf("%s (%s, sender %s, tls %t)", c.Host, auth, c.SenderAddress, c.TLS)
}

// LoadSMTP reads the SMTP_* variables. Unlike the other loaders in this package it returns an
// error instead of falling back, because a half-configured provider would leave Zitadel unable
// to send mail while the install reports success.
func LoadSMTP(getenv func(string) string) (SMTPConfig, error) {
	cfg := SMTPConfig{
		Host:          strings.TrimSpace(getenv("SMTP_HOST")),
		User:          strings.TrimSpace(getenv("SMTP_USER")),
		Password:      getenv("SMTP_PASSWORD"),
		SenderAddress: strings.TrimSpace(getenv("SMTP_SENDER_ADDRESS")),
		SenderName:    strings.TrimSpace(getenv("SMTP_SENDER_NAME")),
		TLS:           true,
	}
	if cfg.SenderName == "" {
		cfg.SenderName = "Stackweaver"
	}
	if raw := strings.TrimSpace(getenv("SMTP_TLS")); raw != "" {
		tls, err := strconv.ParseBool(raw)
		if err != nil {
			return SMTPConfig{}, fmt.Errorf("parsing SMTP_TLS %q: %w", raw, err)
		}
		cfg.TLS = tls
	}
	if cfg.Enabled() && cfg.SenderAddress == "" {
		return SMTPConfig{}, errors.New("SMTP_HOST is set but SMTP_SENDER_ADDRESS is empty: Zitadel needs a sender address to deliver mail")
	}
	return cfg, nil
}

// LoadSelfRegistration reads STACKWEAVER_SELF_REGISTRATION, the operator's wish for open signup.
// It only takes effect when mail can be delivered (#829 D4); unset means closed.
func LoadSelfRegistration(getenv func(string) string) (bool, error) {
	raw := strings.TrimSpace(getenv("STACKWEAVER_SELF_REGISTRATION"))
	if raw == "" {
		return false, nil
	}
	open, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("parsing STACKWEAVER_SELF_REGISTRATION %q: %w", raw, err)
	}
	return open, nil
}
