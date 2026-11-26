package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// normalizeConnectionLabel trims surrounding white space and requires a valid
// UTF-8 name of 1-maxLabelRunes characters without control characters.
func normalizeConnectionLabel(label string) (string, error) {
	invalid := fmt.Errorf("%w: connection name must be 1-%d characters without control characters", ErrInvalid, maxLabelRunes)
	if !utf8.ValidString(label) {
		return "", invalid
	}
	label = strings.TrimSpace(label)
	if label == "" || utf8.RuneCountInString(label) > maxLabelRunes {
		return "", invalid
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return "", invalid
		}
	}
	return label, nil
}

// RenameCredential changes only the display label of one of userID's active app
// credentials and returns its non-secret metadata. The secret hash, kind, share,
// mode and usage are unchanged, so existing clients keep working. Another user's,
// a revoked or an unknown credential is ErrNotFound; an invalid label ErrInvalid.
func (s *Service) RenameCredential(ctx context.Context, userID, credID int64, label string) (CredentialInfo, error) {
	label, err := normalizeConnectionLabel(label)
	if err != nil {
		return CredentialInfo{}, err
	}
	var ci CredentialInfo
	var share, lastUsed sql.NullInt64
	var created int64
	err = s.db.QueryRowContext(ctx, `UPDATE credentials SET label = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL
		RETURNING id, user_id, label, kind, share_id, mode, created_at, last_used_at`, label, credID, userID).
		Scan(&ci.ID, &ci.UserID, &ci.Label, &ci.Kind, &share, &ci.Mode, &created, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return CredentialInfo{}, ErrNotFound
	}
	if err != nil {
		return CredentialInfo{}, err
	}
	ci.ShareID = share.Int64
	ci.CreatedAt = time.UnixMilli(created).UTC()
	if lastUsed.Valid {
		ci.LastUsedAt = time.UnixMilli(lastUsed.Int64).UTC()
	}
	return ci, nil
}
