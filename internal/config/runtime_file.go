package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
)

const (
	runtimeFileName    = "runtime.json"
	maxRuntimeFileSize = 64 << 10
)

// RuntimeConfig stores effective non-secret options and OIDC credentials needed
// to recover a complete server on a new host. The file is always owner-readable.
type RuntimeConfig struct {
	ListenAddr    string             `json:"listenAddr"`
	PublicOrigin  string             `json:"publicOrigin,omitempty"`
	OIDCProviders []OIDCProvider     `json:"oidcProviders,omitempty"`
	Notifications NotificationConfig `json:"notifications,omitempty"`
}

type NotificationConfig struct {
	SMTP        SMTPConfig       `json:"smtp,omitempty"`
	Telegram    TelegramConfig   `json:"telegram,omitempty"`
	AdminAlerts AdminAlertConfig `json:"adminAlerts,omitempty"`
}

type AdminAlertConfig struct {
	BackupSuccess bool `json:"backupSuccess"`
	BackupFailure bool `json:"backupFailure"`
	Security      bool `json:"security"`
}

type SMTPConfig struct {
	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	TLSMode   string `json:"tlsMode,omitempty"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	From      string `json:"from,omitempty"`
	Recipient string `json:"recipient,omitempty"`
}

type TelegramConfig struct {
	BotToken string `json:"botToken,omitempty"`
	ChatID   string `json:"chatId,omitempty"`
}

func (SMTPConfig) String() string       { return "SMTPConfig{credentials:<redacted>}" }
func (SMTPConfig) GoString() string     { return "config.SMTPConfig{credentials:<redacted>}" }
func (TelegramConfig) String() string   { return "TelegramConfig{credentials:<redacted>}" }
func (TelegramConfig) GoString() string { return "config.TelegramConfig{credentials:<redacted>}" }

var runtimeUpdateMu sync.Mutex
var telegramBotToken = regexp.MustCompile(`^[0-9]{5,20}:[A-Za-z0-9_-]{20,200}$`)
var telegramChatID = regexp.MustCompile(`^-?[0-9]{1,20}$`)

func RuntimePath(dataDir string) string { return filepath.Join(dataDir, runtimeFileName) }

func LoadRuntime(path string) (RuntimeConfig, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return RuntimeConfig{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return RuntimeConfig{}, errors.New("runtime config must be a regular file")
	}
	if err := securefile.Check(path, info); err != nil {
		return RuntimeConfig{}, errors.New("runtime config file is not private")
	}
	if info.Size() > maxRuntimeFileSize {
		return RuntimeConfig{}, errors.New("runtime config is too large")
	}
	f, err := os.Open(path)
	if err != nil {
		return RuntimeConfig{}, err
	}
	defer f.Close()
	var cfg RuntimeConfig
	d := json.NewDecoder(io.LimitReader(f, maxRuntimeFileSize+1))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return RuntimeConfig{}, errors.New("runtime config is invalid")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return RuntimeConfig{}, errors.New("runtime config contains trailing data")
	}
	if cfg.ListenAddr == "" {
		return RuntimeConfig{}, errors.New("runtime config listen address is required")
	}
	if err := validateRuntime(cfg); err != nil {
		return RuntimeConfig{}, errors.New("runtime config settings are invalid")
	}
	return cfg, nil
}

func SaveRuntime(path string, cfg RuntimeConfig) error {
	if cfg.ListenAddr == "" {
		return errors.New("runtime config listen address is required")
	}
	if err := validateRuntime(cfg); err != nil {
		return errors.New("runtime config settings are invalid")
	}
	if err := securefile.MkdirAll(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create runtime config directory: %w", err)
	}
	tmp, err := securefile.CreateTemp(filepath.Dir(path), ".runtime-*.tmp")
	if err != nil {
		return fmt.Errorf("create runtime config: %w", err)
	}
	tmpPath := tmp.Name()
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(tmpPath)
		}
	}()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		tmp.Close()
		return fmt.Errorf("write runtime config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync runtime config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close runtime config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish runtime config: %w", err)
	}
	complete = true
	if err := securefile.SyncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync runtime config directory: %w", err)
	}
	return nil
}

// UpdateRuntime applies one serialized, validated update and atomically publishes it.
func UpdateRuntime(path string, update func(*RuntimeConfig) error) error {
	if update == nil {
		return errors.New("runtime config update is required")
	}
	runtimeUpdateMu.Lock()
	defer runtimeUpdateMu.Unlock()
	cfg, err := LoadRuntime(path)
	if err != nil {
		return errors.New("runtime config is unavailable")
	}
	if err := update(&cfg); err != nil {
		return err
	}
	if err := validateRuntime(cfg); err != nil {
		return errors.New("runtime config settings are invalid")
	}
	return SaveRuntime(path, cfg)
}

func validateRuntime(cfg RuntimeConfig) error {
	if err := (Config{ListenAddr: cfg.ListenAddr, DataDir: ".", PublicOrigin: cfg.PublicOrigin}).Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, provider := range cfg.OIDCProviders {
		if seen[provider.ID] || strings.TrimSpace(provider.ClientID) == "" || strings.TrimSpace(provider.ClientSecret) == "" {
			return errors.New("OIDC provider settings are invalid")
		}
		seen[provider.ID] = true
		switch provider.ID {
		case "google":
			if provider.Issuer != GoogleIssuer || provider.Name != "Google" {
				return errors.New("Google provider settings are invalid")
			}
		case "authentik":
			if provider.Name != "Authentik" || len(provider.Issuer) == 0 {
				return errors.New("Authentik provider settings are invalid")
			}
			if _, err := ParseOIDCIssuer(provider.Issuer); err != nil {
				return errors.New("Authentik provider settings are invalid")
			}
		default:
			return errors.New("unsupported OIDC provider")
		}
	}
	if len(cfg.OIDCProviders) > 0 && cfg.PublicOrigin == "" {
		return errors.New("OIDC providers require a public origin")
	}
	if err := validateNotifications(cfg.Notifications); err != nil {
		return err
	}
	return nil
}

func validateNotifications(cfg NotificationConfig) error {
	s := cfg.SMTP
	smtpEmpty := s.Host == "" && s.Port == 0 && s.TLSMode == "" && s.Username == "" && s.Password == "" && s.From == "" && s.Recipient == ""
	if !smtpEmpty {
		from, fromErr := mail.ParseAddress(s.From)
		recipient, recipientErr := mail.ParseAddress(s.Recipient)
		if strings.TrimSpace(s.Host) != s.Host || s.Host == "" || strings.ContainsAny(s.Host, " \t\r\n/@") || s.Port < 1 || s.Port > 65535 || (s.TLSMode != "tls" && s.TLSMode != "starttls") || fromErr != nil || from.Address != s.From || (s.Recipient != "" && (recipientErr != nil || recipient.Address != s.Recipient)) {
			return errors.New("SMTP settings are invalid")
		}
		if (s.Username == "") != (s.Password == "") {
			return errors.New("SMTP authentication settings are incomplete")
		}
	}
	if token := cfg.Telegram.BotToken; token != "" {
		if strings.TrimSpace(token) != token || len(token) > 256 || !telegramBotToken.MatchString(token) {
			return errors.New("Telegram settings are invalid")
		}
	}
	if chatID := cfg.Telegram.ChatID; chatID != "" {
		if strings.TrimSpace(chatID) != chatID || len(chatID) > 24 || !telegramChatID.MatchString(chatID) {
			return errors.New("Telegram settings are invalid")
		}
	}
	return nil
}
