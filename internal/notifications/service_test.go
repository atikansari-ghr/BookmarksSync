package notifications

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
)

func TestAdminSettingsRedactSecretsAndBlankUpdatePreservesThem(t *testing.T) {
	dataDir := t.TempDir()
	runtimePath := config.RuntimePath(dataDir)
	initial := config.RuntimeConfig{ListenAddr: "127.0.0.1:8080", PublicOrigin: "https://bookmarks.example", Notifications: config.NotificationConfig{
		SMTP:     config.SMTPConfig{Host: "mail.example", Port: 587, TLSMode: "starttls", Username: "mailer", Password: "smtp-secret", From: "bookmarks@example.test", Recipient: "admin@example.test"},
		Telegram: config.TelegramConfig{BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", ChatID: "12345678"},
	}}
	if err := config.SaveRuntime(runtimePath, initial); err != nil {
		t.Fatal(err)
	}
	svc := NewService(runtimePath, initial.Notifications)
	view := svc.AdminSettings()
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "smtp-secret") || strings.Contains(string(encoded), initial.Notifications.Telegram.BotToken) {
		t.Fatalf("admin settings leaked credentials: %s", encoded)
	}
	if !view.SMTP.Configured || !view.SMTP.AuthenticationConfigured || !view.Telegram.Configured || !view.Telegram.ChatConfigured {
		t.Fatalf("configured status missing: %#v", view)
	}
	if _, err := svc.UpdateAdminSettings(context.Background(), AdminUpdate{SMTP: &SMTPUpdate{Host: "mail2.example", Port: 465, TLSMode: "tls", Username: "mailer2", From: "bookmarks@example.test"}}); err != nil {
		t.Fatal(err)
	}
	stored, err := config.LoadRuntime(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Notifications.SMTP.Password != "smtp-secret" || stored.Notifications.Telegram.BotToken != initial.Notifications.Telegram.BotToken || stored.PublicOrigin != initial.PublicOrigin {
		t.Fatalf("blank secrets or unrelated settings were not preserved: %#v", stored)
	}
	info, err := os.Stat(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := securefile.Check(runtimePath, info); err != nil {
		t.Fatalf("runtime settings are not private: %v", err)
	}
}

func TestSendSMTPMessageUsesVerifiedImplicitTLSAndNeutralPayload(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey}
	rootCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(rootCert)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{cert}})
	defer tlsListener.Close()
	message := make(chan string, 1)
	serverErr := make(chan error, 1)
	go func() {
		conn, err := tlsListener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		write := func(value string) bool {
			_, err := writer.WriteString(value + "\r\n")
			if err == nil {
				err = writer.Flush()
			}
			return err == nil
		}
		if !write("220 test smtp") {
			serverErr <- fmt.Errorf("greeting write failed")
			return
		}
		var content strings.Builder
		received := false
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				if received {
					return // smtp.Client.Close closes the socket without a QUIT command.
				}
				serverErr <- readErr
				return
			}
			command := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(command, "EHLO "):
				if !write("250 test") {
					return
				}
			case strings.HasPrefix(command, "MAIL FROM:") || strings.HasPrefix(command, "RCPT TO:"):
				if !write("250 accepted") {
					return
				}
			case command == "DATA":
				if !write("354 continue") {
					return
				}
				for {
					data, dataErr := reader.ReadString('\n')
					if dataErr != nil {
						serverErr <- dataErr
						return
					}
					if strings.TrimSpace(data) == "." {
						break
					}
					content.WriteString(data)
				}
				message <- content.String()
				received = true
				if !write("250 queued") {
					return
				}
			case command == "QUIT":
				write("221 bye")
				return
			default:
				if !write("250 ok") {
					return
				}
			}
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	ctx := context.Background()
	if err := sendSMTPMessageWithRoots(ctx, config.SMTPConfig{Host: "127.0.0.1", Port: address.Port, TLSMode: "tls", From: "Bookmarks <bookmarks@example.test>"}, "alice@example.test", "BookmarksSync test", "A neutral test message.", roots); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-message:
		if !strings.Contains(got, "To: <alice@example.test>") || !strings.Contains(got, "A neutral test message.") || strings.Contains(got, "password") {
			t.Fatalf("unexpected SMTP message: %q", got)
		}
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP server did not receive the message")
	}
}

func TestSMTPTestSendsOnlyANeutralMessage(t *testing.T) {
	dataDir := t.TempDir()
	runtimePath := config.RuntimePath(dataDir)
	initial := config.RuntimeConfig{ListenAddr: "127.0.0.1:8080", Notifications: config.NotificationConfig{SMTP: config.SMTPConfig{Host: "mail.example", Port: 465, TLSMode: "tls", Username: "user", Password: "smtp-secret", From: "bookmarks@example.test", Recipient: "admin@example.test"}}}
	if err := config.SaveRuntime(runtimePath, initial); err != nil {
		t.Fatal(err)
	}
	svc := NewService(runtimePath, initial.Notifications)
	var gotTo, gotSubject, gotBody string
	svc.smtpSender = func(_ context.Context, cfg config.SMTPConfig, to, subject, body string) error {
		if cfg.Password != "smtp-secret" {
			t.Fatal("stored SMTP password was not passed to sender")
		}
		gotTo, gotSubject, gotBody = to, subject, body
		return nil
	}
	if err := svc.Test(context.Background(), "smtp"); err != nil {
		t.Fatal(err)
	}
	if gotTo != "admin@example.test" || gotSubject == "" || gotBody == "" || strings.Contains(gotBody, "smtp-secret") {
		t.Fatalf("unsafe SMTP test message: to=%q subject=%q body=%q", gotTo, gotSubject, gotBody)
	}
}

func TestTelegramTestUsesConfiguredBotAndHidesProviderErrors(t *testing.T) {
	token := "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+token+"/sendMessage" || r.Method != http.MethodPost {
			t.Errorf("Telegram request path/method = %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"ok":false,"description":"provider rejected `+token+`"}`)
	}))
	defer server.Close()
	runtimePath := config.RuntimePath(t.TempDir())
	initial := config.RuntimeConfig{ListenAddr: "127.0.0.1:8080", Notifications: config.NotificationConfig{Telegram: config.TelegramConfig{BotToken: token, ChatID: "987654321"}}}
	if err := config.SaveRuntime(runtimePath, initial); err != nil {
		t.Fatal(err)
	}
	svc := NewService(runtimePath, initial.Notifications)
	svc.telegramAPIBase = server.URL
	svc.httpClient = server.Client()
	err := svc.Test(context.Background(), "telegram")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("Telegram provider error leaked or succeeded: %v", err)
	}
}

func TestAdminAlertQueueUsesOnlyEnabledGenericEvents(t *testing.T) {
	runtimePath := config.RuntimePath(t.TempDir())
	initial := config.RuntimeConfig{ListenAddr: "127.0.0.1:8080", Notifications: config.NotificationConfig{
		SMTP:        config.SMTPConfig{Host: "mail.example", Port: 587, TLSMode: "starttls", From: "bookmarks@example.test", Recipient: "admin@example.test"},
		AdminAlerts: config.AdminAlertConfig{BackupFailure: true},
	}}
	if err := config.SaveRuntime(runtimePath, initial); err != nil {
		t.Fatal(err)
	}
	svc := NewService(runtimePath, initial.Notifications)
	type sent struct{ to, subject, body string }
	sentMessages := make(chan sent, 2)
	svc.smtpSender = func(_ context.Context, _ config.SMTPConfig, to, subject, body string) error {
		sentMessages <- sent{to, subject, body}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	if !svc.QueueAdminAlert(AlertBackupFailure, "Backup failed", "Review System backups for details.") {
		t.Fatal("enabled alert was rejected")
	}
	select {
	case got := <-sentMessages:
		if got.to != "admin@example.test" || !strings.Contains(got.subject, "Backup failed") || strings.Contains(got.body, "https://") {
			t.Fatalf("unsafe alert message: %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued alert was not sent")
	}
	if !svc.QueueAdminAlert(AlertSecurity, "Account security settings changed", "Review admin activity.") {
		t.Fatal("disabled category should still queue for the dispatcher to skip")
	}
	select {
	case got := <-sentMessages:
		t.Fatalf("disabled alert category was delivered: %#v", got)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	<-done
}

func TestAdminAlertQueueDrainsOnShutdown(t *testing.T) {
	runtimePath := config.RuntimePath(t.TempDir())
	cfg := config.NotificationConfig{SMTP: config.SMTPConfig{Host: "mail.example", Port: 587, TLSMode: "starttls", From: "bookmarks@example.test", Recipient: "admin@example.test"}, AdminAlerts: config.AdminAlertConfig{BackupFailure: true}}
	if err := config.SaveRuntime(runtimePath, config.RuntimeConfig{ListenAddr: "127.0.0.1:8080", Notifications: cfg}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(runtimePath, cfg)
	sent := 0
	svc.smtpSender = func(context.Context, config.SMTPConfig, string, string, string) error { sent++; return nil }
	if !svc.QueueAdminAlert(AlertBackupFailure, "Backup failed", "Review the backup page.") {
		t.Fatal("alert was not queued")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run shutdown error=%v", err)
	}
	if sent != 1 {
		t.Fatalf("drained alerts=%d, want 1", sent)
	}
}

func TestAdminSettingsCanExplicitlyClearProviderSecrets(t *testing.T) {
	runtimePath := config.RuntimePath(t.TempDir())
	initial := config.RuntimeConfig{ListenAddr: "127.0.0.1:8080", Notifications: config.NotificationConfig{
		SMTP:     config.SMTPConfig{Host: "mail.example", Port: 587, TLSMode: "starttls", Username: "mailer", Password: "smtp-secret", From: "bookmarks@example.test", Recipient: "admin@example.test"},
		Telegram: config.TelegramConfig{BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", ChatID: "12345678"},
	}}
	if err := config.SaveRuntime(runtimePath, initial); err != nil {
		t.Fatal(err)
	}
	svc := NewService(runtimePath, initial.Notifications)
	if _, err := svc.UpdateAdminSettings(context.Background(), AdminUpdate{SMTP: &SMTPUpdate{Clear: true}, Telegram: &TelegramUpdate{Clear: true}}); err != nil {
		t.Fatal(err)
	}
	stored, err := config.LoadRuntime(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Notifications.SMTP.Password != "" || stored.Notifications.Telegram.BotToken != "" {
		t.Fatal("provider secrets remain after explicit clear")
	}
	if _, err := os.Stat(filepath.Dir(runtimePath)); err != nil {
		t.Fatal(err)
	}
}
