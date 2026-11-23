// Package notifications stores provider configuration and sends privacy-safe alerts.
package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
)

type SMTPSettings struct {
	Host                     string `json:"host"`
	Port                     int    `json:"port"`
	TLSMode                  string `json:"tlsMode"`
	From                     string `json:"from"`
	Recipient                string `json:"recipient"`
	Configured               bool   `json:"configured"`
	AuthenticationConfigured bool   `json:"authenticationConfigured"`
}

type TelegramSettings struct {
	Configured     bool `json:"configured"`
	ChatConfigured bool `json:"chatConfigured"`
}

type AdminSettings struct {
	SMTP             SMTPSettings       `json:"smtp"`
	Telegram         TelegramSettings   `json:"telegram"`
	AdminAlerts      AdminAlertSettings `json:"adminAlerts"`
	DeliveryFailures uint64             `json:"deliveryFailures"`
}

type AdminAlertSettings struct {
	BackupSuccess bool `json:"backupSuccess"`
	BackupFailure bool `json:"backupFailure"`
	Security      bool `json:"security"`
}

type AdminAlertUpdate struct {
	BackupSuccess bool `json:"backupSuccess"`
	BackupFailure bool `json:"backupFailure"`
	Security      bool `json:"security"`
}

type SMTPUpdate struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	TLSMode       string `json:"tlsMode"`
	Username      string `json:"username"`
	Password      string `json:"password,omitempty"`
	From          string `json:"from"`
	Recipient     string `json:"recipient"`
	ClearPassword bool   `json:"clearPassword,omitempty"`
	Clear         bool   `json:"clear,omitempty"`
}

type TelegramUpdate struct {
	BotToken string `json:"botToken,omitempty"`
	ChatID   string `json:"chatId,omitempty"`
	Clear    bool   `json:"clear,omitempty"`
}

type AdminUpdate struct {
	SMTP        *SMTPUpdate       `json:"smtp,omitempty"`
	Telegram    *TelegramUpdate   `json:"telegram,omitempty"`
	AdminAlerts *AdminAlertUpdate `json:"adminAlerts,omitempty"`
}

type Service struct {
	mu               sync.RWMutex
	runtimePath      string
	cfg              config.NotificationConfig
	httpClient       *http.Client
	telegramAPIBase  string
	smtpSender       func(context.Context, config.SMTPConfig, string, string, string) error
	queue            chan adminAlert
	deliveryFailures uint64
}

type adminAlert struct {
	Category, Title, Body string
	OccurredAt            time.Time
}

const (
	AlertBackupSuccess = "backup_success"
	AlertBackupFailure = "backup_failure"
	AlertSecurity      = "security"
)

func NewService(runtimePath string, cfg config.NotificationConfig) *Service {
	return &Service{runtimePath: runtimePath, cfg: cfg, httpClient: &http.Client{Timeout: 10 * time.Second}, telegramAPIBase: "https://api.telegram.org", smtpSender: sendSMTPMessage, queue: make(chan adminAlert, 128)}
}

func (s *Service) AdminSettings() AdminSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	smtp := s.cfg.SMTP
	return AdminSettings{
		SMTP: SMTPSettings{Host: smtp.Host, Port: smtp.Port, TLSMode: smtp.TLSMode, From: smtp.From, Recipient: smtp.Recipient,
			Configured: smtp.Host != "" && smtp.From != "" && smtp.Recipient != "", AuthenticationConfigured: smtp.Username != "" && smtp.Password != ""},
		Telegram:         TelegramSettings{Configured: s.cfg.Telegram.BotToken != "", ChatConfigured: s.cfg.Telegram.ChatID != ""},
		AdminAlerts:      AdminAlertSettings{BackupSuccess: s.cfg.AdminAlerts.BackupSuccess, BackupFailure: s.cfg.AdminAlerts.BackupFailure, Security: s.cfg.AdminAlerts.Security},
		DeliveryFailures: s.deliveryFailures,
	}
}

func (s *Service) UpdateAdminSettings(ctx context.Context, update AdminUpdate) (AdminSettings, error) {
	if err := ctx.Err(); err != nil {
		return AdminSettings{}, err
	}
	if update.SMTP == nil && update.Telegram == nil && update.AdminAlerts == nil {
		return AdminSettings{}, errors.New("notification settings update is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var saved config.NotificationConfig
	err := config.UpdateRuntime(s.runtimePath, func(runtime *config.RuntimeConfig) error {
		notify := runtime.Notifications
		if update.SMTP != nil {
			in := *update.SMTP
			noop := !in.Clear && in.Host == "" && in.Username == "" && in.Password == "" && in.From == "" && in.Recipient == ""
			if noop {
				// A form can update Telegram or alert preferences while SMTP remains empty.
			} else if in.Clear {
				if in.Password != "" || in.ClearPassword {
					return errors.New("invalid SMTP clear request")
				}
				notify.SMTP = config.SMTPConfig{}
			} else {
				password := notify.SMTP.Password
				username := notify.SMTP.Username
				if in.ClearPassword {
					if in.Password != "" {
						return errors.New("invalid SMTP password update")
					}
					password = ""
					username = ""
				} else if in.Password != "" {
					password = in.Password
				}
				if in.Username != "" {
					username = in.Username
				}
				recipient := in.Recipient
				if recipient == "" {
					recipient = notify.SMTP.Recipient
				}
				notify.SMTP = config.SMTPConfig{Host: in.Host, Port: in.Port, TLSMode: in.TLSMode, Username: username, Password: password, From: in.From, Recipient: recipient}
			}
		}
		if update.Telegram != nil {
			if update.Telegram.Clear {
				if update.Telegram.BotToken != "" || update.Telegram.ChatID != "" {
					return errors.New("invalid Telegram clear request")
				}
				notify.Telegram = config.TelegramConfig{}
			} else if update.Telegram.BotToken != "" {
				notify.Telegram.BotToken = update.Telegram.BotToken
			}
			if !update.Telegram.Clear && update.Telegram.ChatID != "" {
				notify.Telegram.ChatID = update.Telegram.ChatID
			}
		}
		if update.AdminAlerts != nil {
			notify.AdminAlerts = config.AdminAlertConfig{BackupSuccess: update.AdminAlerts.BackupSuccess, BackupFailure: update.AdminAlerts.BackupFailure, Security: update.AdminAlerts.Security}
		}
		runtime.Notifications = notify
		saved = notify
		return nil
	})
	if err != nil {
		return AdminSettings{}, err
	}
	s.cfg = saved
	return s.adminSettingsLocked(), nil
}

func (s *Service) adminSettingsLocked() AdminSettings {
	smtp := s.cfg.SMTP
	return AdminSettings{
		SMTP: SMTPSettings{Host: smtp.Host, Port: smtp.Port, TLSMode: smtp.TLSMode, From: smtp.From, Recipient: smtp.Recipient,
			Configured: smtp.Host != "" && smtp.From != "" && smtp.Recipient != "", AuthenticationConfigured: smtp.Username != "" && smtp.Password != ""},
		Telegram:         TelegramSettings{Configured: s.cfg.Telegram.BotToken != "", ChatConfigured: s.cfg.Telegram.ChatID != ""},
		AdminAlerts:      AdminAlertSettings{BackupSuccess: s.cfg.AdminAlerts.BackupSuccess, BackupFailure: s.cfg.AdminAlerts.BackupFailure, Security: s.cfg.AdminAlerts.Security},
		DeliveryFailures: s.deliveryFailures,
	}
}

func (s *Service) Test(ctx context.Context, channel string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	switch channel {
	case "smtp":
		if cfg.SMTP.Host == "" || cfg.SMTP.From == "" || cfg.SMTP.Recipient == "" {
			return errors.New("SMTP is not configured")
		}
		if err := s.smtpSender(ctx, cfg.SMTP, cfg.SMTP.Recipient, "BookmarksSync notification test", "This is a test notification from your BookmarksSync server."); err != nil {
			return errors.New("SMTP notification test failed")
		}
		return nil
	case "telegram":
		if cfg.Telegram.BotToken == "" || cfg.Telegram.ChatID == "" {
			return errors.New("Telegram is not configured")
		}
		return s.sendTelegram(ctx, cfg.Telegram.BotToken, cfg.Telegram.ChatID, "BookmarksSync notification test\nThis is a test notification from your server.")
	default:
		return errors.New("notification channel is invalid")
	}
}

// SendVerification delivers only a short-lived channel confirmation code.
func (s *Service) SendVerification(ctx context.Context, channel, destination, code string) error {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	switch channel {
	case "email":
		if cfg.SMTP.Host == "" || cfg.SMTP.From == "" {
			return errors.New("email notifications are unavailable")
		}
		return s.smtpSender(ctx, cfg.SMTP, destination, "BookmarksSync channel verification", "Your verification code is: "+code+"\nIt expires in 10 minutes. If you did not request this, ignore this message.")
	case "telegram":
		if cfg.Telegram.BotToken == "" {
			return errors.New("telegram notifications are unavailable")
		}
		return s.sendTelegram(ctx, cfg.Telegram.BotToken, destination, "BookmarksSync verification code: "+code+"\nIt expires in 10 minutes.")
	default:
		return errors.New("notification channel is invalid")
	}
}

// SendNotice sends one of the fixed, generic user notices. Event details supplied
// by request handlers are intentionally not accepted here.
func (s *Service) SendNotice(ctx context.Context, channel, destination, category string, occurredAt time.Time) error {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	subject, summary := "BookmarksSync account notice", "Review your BookmarksSync account settings for a recent change."
	if category == "sharing" {
		subject = "BookmarksSync sharing notice"
		summary = "A sharing setting for your account changed. Review your sharing page for details."
	}
	body := summary + "\nOccurred: " + occurredAt.UTC().Format(time.RFC3339)
	switch channel {
	case "email":
		if cfg.SMTP.Host == "" || cfg.SMTP.From == "" {
			return errors.New("email notifications are unavailable")
		}
		return s.smtpSender(ctx, cfg.SMTP, destination, subject, body)
	case "telegram":
		if cfg.Telegram.BotToken == "" {
			return errors.New("telegram notifications are unavailable")
		}
		return s.sendTelegram(ctx, cfg.Telegram.BotToken, destination, subject+"\n"+body)
	default:
		return errors.New("notification channel is invalid")
	}
}

// QueueAdminAlert drops only when the bounded queue is full; provider work never
// blocks the request or background operation that produced the event.
func (s *Service) QueueAdminAlert(category, title, body string) bool {
	if title == "" || body == "" || len(title) > 120 || len(body) > 400 || strings.ContainsAny(title+body, "\x00") {
		return false
	}
	if category != AlertBackupSuccess && category != AlertBackupFailure && category != AlertSecurity {
		return false
	}
	select {
	case s.queue <- adminAlert{Category: category, Title: title, Body: body, OccurredAt: time.Now().UTC()}:
		return true
	default:
		s.mu.Lock()
		s.deliveryFailures++
		s.mu.Unlock()
		return false
	}
}

func (s *Service) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			s.drainQueue(nil)
			return ctx.Err()
		case alert := <-s.queue:
			if ctx.Err() != nil {
				s.drainQueue(&alert)
				return ctx.Err()
			}
			if err := s.deliverAdminAlert(ctx, alert); err != nil {
				s.mu.Lock()
				s.deliveryFailures++
				s.mu.Unlock()
			}
		}
	}
}

func (s *Service) drainQueue(first *adminAlert) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	deliver := func(alert adminAlert) {
		if err := s.deliverAdminAlert(ctx, alert); err != nil {
			s.mu.Lock()
			s.deliveryFailures++
			s.mu.Unlock()
		}
	}
	if first != nil {
		deliver(*first)
	}
	for {
		select {
		case alert := <-s.queue:
			deliver(alert)
		default:
			return
		}
	}
}

func (s *Service) deliverAdminAlert(ctx context.Context, alert adminAlert) error {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	enabled := (alert.Category == AlertBackupSuccess && cfg.AdminAlerts.BackupSuccess) || (alert.Category == AlertBackupFailure && cfg.AdminAlerts.BackupFailure) || (alert.Category == AlertSecurity && cfg.AdminAlerts.Security)
	if !enabled {
		return nil
	}
	var deliveryErr error
	body := alert.Body + "\nOccurred: " + alert.OccurredAt.UTC().Format(time.RFC3339)
	if cfg.SMTP.Recipient != "" {
		if err := s.smtpSender(ctx, cfg.SMTP, cfg.SMTP.Recipient, "BookmarksSync: "+alert.Title, body); err != nil {
			deliveryErr = errors.New("email alert delivery failed")
		}
	}
	if cfg.Telegram.BotToken != "" && cfg.Telegram.ChatID != "" {
		if err := s.sendTelegram(ctx, cfg.Telegram.BotToken, cfg.Telegram.ChatID, "BookmarksSync: "+alert.Title+"\n"+body); err != nil {
			deliveryErr = errors.New("Telegram alert delivery failed")
		}
	}
	return deliveryErr
}

func (s *Service) DeliveryFailures() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deliveryFailures
}

func (s *Service) sendTelegram(ctx context.Context, token, chatID, text string) error {
	endpoint := strings.TrimRight(s.telegramAPIBase, "/") + "/bot" + token + "/sendMessage"
	body, err := json.Marshal(struct {
		ChatID         string `json:"chat_id"`
		Text           string `json:"text"`
		DisablePreview bool   `json:"disable_web_page_preview"`
	}{ChatID: chatID, Text: text, DisablePreview: true})
	if err != nil {
		return errors.New("Telegram notification could not be prepared")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return errors.New("Telegram notification could not be prepared")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return errors.New("Telegram notification request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || resp.StatusCode != http.StatusOK {
		return errors.New("Telegram notification provider rejected the request")
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(data, &result) != nil || !result.OK {
		return errors.New("Telegram notification provider rejected the request")
	}
	return nil
}

func (s *Service) String() string {
	return fmt.Sprintf("NotificationService{runtimeConfig:<private>, providers:%s}", strings.Join(configuredProviderNames(s.AdminSettings()), ","))
}
func (s *Service) GoString() string { return s.String() }

func configuredProviderNames(settings AdminSettings) []string {
	var names []string
	if settings.SMTP.Configured {
		names = append(names, "smtp")
	}
	if settings.Telegram.Configured {
		names = append(names, "telegram")
	}
	return names
}
