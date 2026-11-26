package notifications

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
)

const providerTimeout = 10 * time.Second

func sendSMTPMessage(ctx context.Context, cfg config.SMTPConfig, recipient, subject, body string) error {
	return sendSMTPMessageWithRoots(ctx, cfg, recipient, subject, body, nil)
}

func sendSMTPMessageWithRoots(ctx context.Context, cfg config.SMTPConfig, recipient, subject, body string, roots *x509.CertPool) error {
	from, err := mail.ParseAddress(cfg.From)
	if err != nil || strings.ContainsAny(subject, "\r\n") || strings.ContainsAny(body, "\x00") {
		return errors.New("SMTP message is invalid")
	}
	to, err := mail.ParseAddress(recipient)
	if err != nil {
		return errors.New("SMTP recipient is invalid")
	}
	if cfg.Host == "" || cfg.Port < 1 || cfg.Port > 65535 || (cfg.TLSMode != "tls" && cfg.TLSMode != "starttls") {
		return errors.New("SMTP settings are invalid")
	}
	address := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	dialer := &net.Dialer{Timeout: providerTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return errors.New("SMTP connection failed")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(providerTimeout))
	}
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	defer conn.Close()
	if cfg.TLSMode == "tls" {
		tlsConn := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.Host, RootCAs: roots})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return errors.New("SMTP TLS handshake failed")
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return errors.New("SMTP server handshake failed")
	}
	defer client.Close()
	if err := client.Hello("bookmarksync"); err != nil {
		return errors.New("SMTP greeting failed")
	}
	if cfg.TLSMode == "starttls" {
		if err := client.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.Host, RootCAs: roots}); err != nil {
			return errors.New("SMTP STARTTLS is required but unavailable")
		}
	}
	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return errors.New("SMTP sender was rejected")
	}
	if err := client.Rcpt(to.Address); err != nil {
		return errors.New("SMTP recipient was rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP message was rejected")
	}
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", from.String(), to.String(), subject, body)
	if _, err := writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return errors.New("SMTP message write failed")
	}
	if err := writer.Close(); err != nil {
		return errors.New("SMTP message was rejected")
	}
	return nil
}
