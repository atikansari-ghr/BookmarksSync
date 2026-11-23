package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/notifications"
)

func TestAdminNotificationSettingsAreProtectedAndRedacted(t *testing.T) {
	e := newTEnv(t)
	if rec := e.do("GET", "/api/v1/admin/notifications", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous settings GET = %d %s", rec.Code, rec.Body.String())
	}
	alice := e.login(e.alice)
	if rec := e.do("GET", "/api/v1/admin/notifications", nil, alice.mods()...); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin settings GET = %d %s", rec.Code, rec.Body.String())
	}
	root := e.login(e.root)
	initial := e.do("GET", "/api/v1/admin/notifications", nil, root.mods()...)
	if initial.Code != http.StatusOK || strings.Contains(initial.Body.String(), "password") || strings.Contains(initial.Body.String(), "botToken") || !strings.Contains(initial.Body.String(), `"userDeliveryFailures":0`) {
		t.Fatalf("admin settings response is unsafe: %d %s", initial.Code, initial.Body.String())
	}
	update := notifications.AdminUpdate{
		SMTP:     &notifications.SMTPUpdate{Host: "smtp.example.test", Port: 465, TLSMode: "tls", Username: "mailer", Password: "smtp-private", From: "bookmarks@example.test", Recipient: "admin@example.test"},
		Telegram: &notifications.TelegramUpdate{BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", ChatID: "987654321"},
	}
	updated := e.do("PUT", "/api/v1/admin/notifications", update, root.mods()...)
	if updated.Code != http.StatusOK || strings.Contains(updated.Body.String(), "smtp-private") || strings.Contains(updated.Body.String(), update.Telegram.BotToken) || strings.Contains(updated.Body.String(), "987654321") {
		t.Fatalf("notification update response is unsafe: %d %s", updated.Code, updated.Body.String())
	}
	if !strings.Contains(updated.Body.String(), `"configured":true`) || !strings.Contains(updated.Body.String(), `"chatConfigured":true`) {
		t.Fatalf("safe provider status missing: %s", updated.Body.String())
	}
	stored := e.notifications.AdminSettings()
	if !stored.SMTP.Configured || !stored.Telegram.Configured {
		t.Fatalf("settings were not persisted: %#v", stored)
	}
}

func TestAdminNotificationTestEndpointUsesGenericErrors(t *testing.T) {
	e := newTEnv(t)
	root := e.login(e.root)
	if rec := e.do("POST", "/api/v1/admin/notifications/test", map[string]string{"channel": "smtp"}, root.mods()...); rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "smtp") {
		t.Fatalf("test endpoint error should be generic: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/v1/admin/notifications/test", map[string]string{"channel": "unknown"}, root.mods()...); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown test channel = %d %s", rec.Code, rec.Body.String())
	}
}

func TestUserNotificationSettingsArePrivateAndRequireVerifiedProvider(t *testing.T) {
	e := newTEnv(t)
	alice := e.login(e.alice)
	bob := e.login(e.bob)
	if rec := e.do("GET", "/api/v1/me/notifications", nil, alice.mods()...); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"channels":[]`) {
		t.Fatalf("initial user notifications: %d %s", rec.Code, rec.Body.String())
	}
	updated := e.do("PATCH", "/api/v1/me/notifications", map[string]bool{"securityAlerts": true, "sharingAlerts": false}, alice.mods()...)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"securityAlerts":true`) {
		t.Fatalf("save preferences: %d %s", updated.Code, updated.Body.String())
	}
	other := e.do("GET", "/api/v1/me/notifications", nil, bob.mods()...)
	if other.Code != http.StatusOK || strings.Contains(other.Body.String(), `"securityAlerts":true`) {
		t.Fatalf("notification preferences crossed users: %d %s", other.Code, other.Body.String())
	}
	failed := e.do("POST", "/api/v1/me/notifications/bindings", map[string]string{"kind": "email", "destination": "private@example.test"}, alice.mods()...)
	if failed.Code != http.StatusBadRequest || strings.Contains(failed.Body.String(), "private@example.test") || strings.Contains(failed.Body.String(), "provider") {
		t.Fatalf("unavailable verification response was not generic: %d %s", failed.Code, failed.Body.String())
	}
}
