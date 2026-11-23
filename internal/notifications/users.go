package notifications

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var ErrUserNotification = errors.New("notification request could not be completed")

type VerificationSender interface {
	SendVerification(context.Context, string, string, string) error
}
type UserNoticeSender interface {
	SendNotice(context.Context, string, string, string, time.Time) error
}

type UserChannel struct {
	Kind        string    `json:"kind"`
	Destination string    `json:"destination"`
	VerifiedAt  time.Time `json:"verifiedAt"`
}

type UserSettings struct {
	Channels       []UserChannel `json:"channels"`
	SecurityAlerts bool          `json:"securityAlerts"`
	SharingAlerts  bool          `json:"sharingAlerts"`
}

type UserService struct {
	db               *sql.DB
	sender           VerificationSender
	noticeSender     UserNoticeSender
	now              func() time.Time
	queue            chan userAlert
	deliveryFailures atomic.Uint64
}

type userAlert struct {
	userID     int64
	category   string
	occurredAt time.Time
}

func NewUserService(db *sql.DB, sender VerificationSender, now func() time.Time) *UserService {
	if now == nil {
		now = time.Now
	}
	noticeSender, _ := sender.(UserNoticeSender)
	return &UserService{db: db, sender: sender, noticeSender: noticeSender, now: now, queue: make(chan userAlert, 128)}
}

func (s *UserService) QueueAlert(userID int64, category string) bool {
	if userID <= 0 || (category != "security" && category != "sharing") {
		return false
	}
	select {
	case s.queue <- userAlert{userID: userID, category: category, occurredAt: s.now().UTC()}:
		return true
	default:
		s.deliveryFailures.Add(1)
		return false
	}
}

func (s *UserService) DeliveryFailures() uint64 { return s.deliveryFailures.Load() }

func (s *UserService) Run(ctx context.Context) error {
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
			s.deliverAlert(ctx, alert)
		}
	}
}

func (s *UserService) drainQueue(first *userAlert) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if first != nil {
		s.deliverAlert(ctx, *first)
	}
	for {
		select {
		case alert := <-s.queue:
			s.deliverAlert(ctx, alert)
		default:
			return
		}
	}
}

func (s *UserService) deliverAlert(ctx context.Context, alert userAlert) {
	if s.noticeSender == nil {
		return
	}
	column := "security_alerts"
	if alert.category == "sharing" {
		column = "sharing_alerts"
	}
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT p.`+column+` FROM notification_preferences p JOIN users u ON u.id=p.user_id WHERE p.user_id=? AND u.active=1`, alert.userID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && enabled != 1) {
		return
	}
	if err != nil {
		s.deliveryFailures.Add(1)
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind,destination FROM notification_channels WHERE user_id=?`, alert.userID)
	if err != nil {
		s.deliveryFailures.Add(1)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kind, destination string
		if rows.Scan(&kind, &destination) != nil {
			s.deliveryFailures.Add(1)
			continue
		}
		if s.noticeSender.SendNotice(ctx, kind, destination, alert.category, alert.occurredAt) != nil {
			s.deliveryFailures.Add(1)
		}
	}
	if rows.Err() != nil {
		s.deliveryFailures.Add(1)
	}
}

func (s *UserService) Settings(ctx context.Context, userID int64) (UserSettings, error) {
	out := UserSettings{Channels: []UserChannel{}}
	rows, err := s.db.QueryContext(ctx, `SELECT kind,destination,verified_at FROM notification_channels WHERE user_id=? ORDER BY kind`, userID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c UserChannel
		var at int64
		if err := rows.Scan(&c.Kind, &c.Destination, &at); err != nil {
			rows.Close()
			return out, err
		}
		c.VerifiedAt = time.UnixMilli(at).UTC()
		out.Channels = append(out.Channels, c)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	var security, sharing int
	err = s.db.QueryRowContext(ctx, `SELECT security_alerts,sharing_alerts FROM notification_preferences WHERE user_id=?`, userID).Scan(&security, &sharing)
	if err != nil && err != sql.ErrNoRows {
		return out, err
	}
	out.SecurityAlerts, out.SharingAlerts = security == 1, sharing == 1
	return out, nil
}

func (s *UserService) SetPreferences(ctx context.Context, userID int64, security, sharing bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification_preferences(user_id,security_alerts,sharing_alerts) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET security_alerts=excluded.security_alerts,sharing_alerts=excluded.sharing_alerts`, userID, boolInt(security), boolInt(sharing))
	return err
}

func (s *UserService) Begin(ctx context.Context, userID int64, kind, destination string) error {
	destination = strings.TrimSpace(destination)
	if !validChannelDestination(kind, destination) || s.sender == nil {
		return ErrUserNotification
	}
	n, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return ErrUserNotification
	}
	code := fmt.Sprintf("%08d", n.Int64())
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return ErrUserNotification
	}
	var rateToken [16]byte
	if _, err := rand.Read(rateToken[:]); err != nil {
		return ErrUserNotification
	}
	hash := challengeHash(salt[:], code)
	destinationHash := sha256.Sum256([]byte(kind + "\x00" + strings.ToLower(destination)))
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM notification_rate_limits WHERE last_sent_at < ?`, now.Add(-24*time.Hour).UnixMilli()); err != nil {
		return err
	}
	var lastSent int64
	err = tx.QueryRowContext(ctx, `SELECT last_sent_at FROM notification_rate_limits WHERE kind=? AND destination_hash=?`, kind, destinationHash[:]).Scan(&lastSent)
	if err == nil && now.Sub(time.UnixMilli(lastSent)) < time.Minute {
		return ErrUserNotification
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	var created int64
	err = tx.QueryRowContext(ctx, `SELECT created_at FROM notification_challenges WHERE user_id=? AND kind=?`, userID, kind).Scan(&created)
	if err == nil && now.Sub(time.UnixMilli(created)) < time.Minute {
		return ErrUserNotification
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notification_challenges(user_id,kind,destination,salt,code_hash,expires_at,attempts,created_at) VALUES(?,?,?,?,?,?,0,?) ON CONFLICT(user_id,kind) DO UPDATE SET destination=excluded.destination,salt=excluded.salt,code_hash=excluded.code_hash,expires_at=excluded.expires_at,attempts=0,created_at=excluded.created_at`, userID, kind, destination, salt[:], hash[:], now.Add(10*time.Minute).UnixMilli(), now.UnixMilli())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notification_rate_limits(kind,destination_hash,request_token,last_sent_at) VALUES(?,?,?,?) ON CONFLICT(kind,destination_hash) DO UPDATE SET request_token=excluded.request_token,last_sent_at=excluded.last_sent_at`, kind, destinationHash[:], rateToken[:], now.UnixMilli())
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = s.sender.SendVerification(ctx, kind, destination, code); err != nil {
		_, _ = s.db.ExecContext(context.Background(), `DELETE FROM notification_challenges WHERE user_id=? AND kind=? AND code_hash=?`, userID, kind, hash[:])
		_, _ = s.db.ExecContext(context.Background(), `DELETE FROM notification_rate_limits WHERE kind=? AND destination_hash=? AND request_token=?`, kind, destinationHash[:], rateToken[:])
		return ErrUserNotification
	}
	return nil
}

func (s *UserService) Confirm(ctx context.Context, userID int64, kind, code string) error {
	if (kind != "email" && kind != "telegram") || len(code) != 8 {
		return ErrUserNotification
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var dest string
	var salt, expected []byte
	var expires int64
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT destination,salt,code_hash,expires_at,attempts FROM notification_challenges WHERE user_id=? AND kind=?`, userID, kind).Scan(&dest, &salt, &expected, &expires, &attempts)
	if err != nil {
		return ErrUserNotification
	}
	now := s.now().UTC()
	actual := challengeHash(salt, code)
	if now.UnixMilli() > expires || attempts >= 5 || subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		_, _ = tx.ExecContext(ctx, `UPDATE notification_challenges SET attempts=MIN(attempts+1,5) WHERE user_id=? AND kind=?`, userID, kind)
		_ = tx.Commit()
		return ErrUserNotification
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notification_channels(user_id,kind,destination,verified_at) VALUES(?,?,?,?) ON CONFLICT(user_id,kind) DO UPDATE SET destination=excluded.destination,verified_at=excluded.verified_at`, userID, kind, dest, now.UnixMilli())
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM notification_challenges WHERE user_id=? AND kind=?`, userID, kind); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *UserService) Remove(ctx context.Context, userID int64, kind string) error {
	if kind != "email" && kind != "telegram" {
		return ErrUserNotification
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM notification_challenges WHERE user_id=? AND kind=?`, userID, kind); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM notification_channels WHERE user_id=? AND kind=?`, userID, kind); err != nil {
		return err
	}
	return tx.Commit()
}

func validChannelDestination(kind, destination string) bool {
	switch kind {
	case "email":
		a, err := mail.ParseAddress(destination)
		return err == nil && a.Address == destination && len(destination) <= 254
	case "telegram":
		return len(destination) > 0 && len(destination) <= 20 && regexp.MustCompile(`^-?[0-9]+$`).MatchString(destination)
	default:
		return false
	}
}

func challengeHash(salt []byte, code string) [32]byte {
	return sha256.Sum256(append(append([]byte(nil), salt...), []byte(code)...))
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
