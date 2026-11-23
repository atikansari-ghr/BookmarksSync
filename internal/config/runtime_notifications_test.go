package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRuntimeNotificationSettingsAreAtomicAndPreserveOtherFields(t *testing.T) {
	path := RuntimePath(t.TempDir())
	want := RuntimeConfig{ListenAddr: "127.0.0.1:8080", PublicOrigin: "https://bookmarks.example", OIDCProviders: []OIDCProvider{{ID: "google", Name: "Google", Issuer: GoogleIssuer, ClientID: "client-id", ClientSecret: "oidc-secret"}}}
	if err := SaveRuntime(path, want); err != nil {
		t.Fatal(err)
	}
	if err := UpdateRuntime(path, func(cfg *RuntimeConfig) error {
		cfg.Notifications.SMTP = SMTPConfig{Host: "mail.example", Port: 587, TLSMode: "starttls", Username: "mailer", Password: "smtp-secret", From: "bookmarks@example.test", Recipient: "admin@example.test"}
		cfg.Notifications.Telegram.BotToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRuntime(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ListenAddr != want.ListenAddr || got.PublicOrigin != want.PublicOrigin || len(got.OIDCProviders) != 1 || got.OIDCProviders[0].ClientSecret != "oidc-secret" {
		t.Fatalf("unrelated runtime settings changed: %#v", got)
	}
	if got.Notifications.SMTP.Password != "smtp-secret" || got.Notifications.Telegram.BotToken != "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi" {
		t.Fatalf("notification secrets did not persist: %#v", got.Notifications)
	}
	if err := UpdateRuntime(path, func(cfg *RuntimeConfig) error { return errors.New("reject update") }); err == nil {
		t.Fatal("rejected update unexpectedly succeeded")
	}
	got, err = LoadRuntime(path)
	if err != nil || got.Notifications.SMTP.Password != "smtp-secret" {
		t.Fatalf("failed update changed runtime settings: %#v, %v", got, err)
	}
}

func TestRuntimeNotificationConfigValidationDoesNotEchoSecrets(t *testing.T) {
	for name, cfg := range map[string]RuntimeConfig{
		"SMTP requires TLS": {ListenAddr: "127.0.0.1:8080", Notifications: NotificationConfig{SMTP: SMTPConfig{Host: "mail.example", Port: 25, TLSMode: "none", Username: "user", Password: "smtp-secret", From: "bookmarks@example.test", Recipient: "admin@example.test"}}},
		"invalid port":      {ListenAddr: "127.0.0.1:8080", Notifications: NotificationConfig{SMTP: SMTPConfig{Host: "mail.example", Port: 70000, TLSMode: "tls", From: "bookmarks@example.test", Recipient: "admin@example.test"}}},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateRuntime(cfg)
			if err == nil || strings.Contains(err.Error(), "smtp-secret") {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestNotificationConfigFormattingRedactsSecrets(t *testing.T) {
	smtp := SMTPConfig{Username: "mail-user", Password: "smtp-secret"}
	telegram := TelegramConfig{BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"}
	for _, text := range []string{fmt.Sprint(smtp), fmt.Sprintf("%#v", smtp), fmt.Sprint(telegram), fmt.Sprintf("%#v", telegram)} {
		if strings.Contains(text, "smtp-secret") || strings.Contains(text, telegram.BotToken) {
			t.Fatalf("formatted notification settings exposed credentials: %s", text)
		}
	}
}
