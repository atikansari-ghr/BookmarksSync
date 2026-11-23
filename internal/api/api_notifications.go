package api

import (
	"net/http"
	"strings"

	"github.com/atikansari-ghr/BookmarksSync/internal/notifications"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

func (a *API) serveNotifications(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/admin/notifications") {
		return false
	}
	if a.notifications == nil {
		server.WriteError(w, http.StatusServiceUnavailable, "notifications_unavailable", "notification settings are unavailable")
		return true
	}
	switch {
	case r.URL.Path == "/api/v1/admin/notifications" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, a.adminNotificationSettings(a.notifications.AdminSettings()))
	case r.URL.Path == "/api/v1/admin/notifications" && r.Method == http.MethodPut:
		var update notifications.AdminUpdate
		if !decodeJSON(w, r, &update) {
			return true
		}
		settings, err := a.notifications.UpdateAdminSettings(r.Context(), update)
		if err != nil {
			server.WriteError(w, http.StatusBadRequest, "invalid_notification_settings", "notification settings are invalid")
		} else {
			writeJSON(w, http.StatusOK, a.adminNotificationSettings(settings))
		}
	case r.URL.Path == "/api/v1/admin/notifications/test" && r.Method == http.MethodPost:
		var request struct {
			Channel string `json:"channel"`
		}
		if !decodeRequiredJSON(w, r, &request, "channel") {
			return true
		}
		if request.Channel != "smtp" && request.Channel != "telegram" {
			server.WriteError(w, http.StatusBadRequest, "invalid_channel", "notification channel is invalid")
			return true
		}
		if err := a.notifications.Test(r.Context(), request.Channel); err != nil {
			server.WriteError(w, http.StatusBadGateway, "notification_test_failed", "test notification could not be delivered")
		} else {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		}
	default:
		server.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	return true
}

func (a *API) adminNotificationSettings(settings notifications.AdminSettings) map[string]any {
	userFailures := uint64(0)
	if a.notificationUsers != nil {
		userFailures = a.notificationUsers.DeliveryFailures()
	}
	return map[string]any{"smtp": settings.SMTP, "telegram": settings.Telegram, "adminAlerts": settings.AdminAlerts, "deliveryFailures": settings.DeliveryFailures, "userDeliveryFailures": userFailures}
}

func (a *API) serveUserNotifications(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/me/notifications") {
		return false
	}
	if a.notificationUsers == nil {
		server.WriteError(w, http.StatusServiceUnavailable, "notifications_unavailable", "notification settings are unavailable")
		return true
	}
	uid := principalFrom(r).user.ID
	switch {
	case r.URL.Path == "/api/v1/me/notifications" && r.Method == http.MethodGet:
		settings, err := a.notificationUsers.Settings(r.Context(), uid)
		if err != nil {
			server.WriteError(w, 500, "notification_settings_failed", "notification settings could not be loaded")
		} else {
			writeJSON(w, 200, settings)
		}
	case r.URL.Path == "/api/v1/me/notifications" && r.Method == http.MethodPatch:
		var in struct {
			Security bool `json:"securityAlerts"`
			Sharing  bool `json:"sharingAlerts"`
		}
		if !decodeRequiredJSON(w, r, &in, "securityAlerts", "sharingAlerts") {
			return true
		}
		if err := a.notificationUsers.SetPreferences(r.Context(), uid, in.Security, in.Sharing); err != nil {
			server.WriteError(w, 500, "notification_settings_failed", "notification settings could not be saved")
		} else if settings, e := a.notificationUsers.Settings(r.Context(), uid); e != nil {
			server.WriteError(w, 500, "notification_settings_failed", "notification settings could not be loaded")
		} else {
			writeJSON(w, 200, settings)
		}
	case r.URL.Path == "/api/v1/me/notifications/bindings" && r.Method == http.MethodPost:
		var in struct {
			Kind        string `json:"kind"`
			Destination string `json:"destination"`
		}
		if !decodeRequiredJSON(w, r, &in, "kind", "destination") {
			return true
		}
		if err := a.notificationUsers.Begin(r.Context(), uid, in.Kind, in.Destination); err != nil {
			server.WriteError(w, 400, "verification_unavailable", "verification could not be started; check the address and try later")
		} else {
			writeJSON(w, 202, map[string]string{"status": "verification_sent"})
		}
	case r.URL.Path == "/api/v1/me/notifications/bindings/confirm" && r.Method == http.MethodPost:
		var in struct {
			Kind string `json:"kind"`
			Code string `json:"code"`
		}
		if !decodeRequiredJSON(w, r, &in, "kind", "code") {
			return true
		}
		if err := a.notificationUsers.Confirm(r.Context(), uid, in.Kind, in.Code); err != nil {
			server.WriteError(w, 400, "verification_failed", "verification code is invalid or expired")
		} else if settings, e := a.notificationUsers.Settings(r.Context(), uid); e != nil {
			server.WriteError(w, 500, "notification_settings_failed", "notification settings could not be loaded")
		} else {
			a.queueUserNotification(uid, "security")
			writeJSON(w, 200, settings)
		}
	case strings.HasPrefix(r.URL.Path, "/api/v1/me/notifications/bindings/") && r.Method == http.MethodDelete:
		kind := strings.TrimPrefix(r.URL.Path, "/api/v1/me/notifications/bindings/")
		if kind == "" || strings.Contains(kind, "/") {
			server.WriteError(w, 404, "not_found", "route not found")
			return true
		}
		if err := a.notificationUsers.Remove(r.Context(), uid, kind); err != nil {
			server.WriteError(w, 400, "invalid_channel", "notification channel is invalid")
		} else {
			a.queueUserNotification(uid, "security")
			empty(w)
		}
	default:
		server.WriteError(w, 405, "method_not_allowed", "method not allowed")
	}
	return true
}
