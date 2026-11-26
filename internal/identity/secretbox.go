package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// The instance key encrypts reversible secrets (TOTP seeds). It is 32 random bytes in
// a private file in the data directory. A full-system backup MUST include this file:
// without it every encrypted TOTP seed is unrecoverable and the affected users must
// re-enroll or be reset by an administrator.
const (
	InstanceKeyFile = "instance.key"
	InstanceKeySize = 32
)

// ErrNotConfigured reports that a feature needs configuration the service lacks (an
// instance key for TOTP, a relying party for passkeys).
var ErrNotConfigured = errors.New("not configured")

// LoadOrCreateInstanceKey returns the install's instance key from dataDir, creating it
// with owner-only permissions (0600 on Unix; a protected DACL granting only the current
// user on Windows) if it does not exist. An existing key that is malformed, unreadable,
// not a regular file, or accessible to other principals is an error: it is never
// repaired or replaced, because replacing it would orphan every encrypted seed. Errors
// never include key material.
func LoadOrCreateInstanceKey(dataDir string) ([]byte, error) {
	if fi, err := os.Stat(dataDir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("instance key: data directory %q is not available", dataDir)
	}
	path := filepath.Join(dataDir, InstanceKeyFile)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return createInstanceKey(path)
	} else if err != nil {
		return nil, fmt.Errorf("instance key: %s: %w", path, err)
	}
	return loadInstanceKey(path)
}

// RestoreInstanceKey installs a previously validated instance key using the
// platform's private-file creation rules. It refuses to replace any existing key.
func RestoreInstanceKey(dataDir string, key []byte) error {
	if err := validKey(key); err != nil {
		return fmt.Errorf("instance key is malformed: %w", err)
	}
	if fi, err := os.Stat(dataDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("instance key: data directory %q is not available", dataDir)
	}
	path := filepath.Join(dataDir, InstanceKeyFile)
	f, err := createPrivateFile(path)
	if err != nil {
		return fmt.Errorf("instance key: cannot create private restored key: %w", err)
	}
	_, writeErr := f.Write(key)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.New("instance key: cannot write restored key")
	}
	if _, err := loadInstanceKey(path); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("instance key: restored key is not private: %w", err)
	}
	return nil
}

func loadInstanceKey(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("instance key: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("instance key: %s is not a regular file", path)
	}
	if err := checkKeyFilePrivate(path, fi); err != nil {
		return nil, fmt.Errorf("instance key: %s is not private (%v); restrict it to the service account and do not delete it", path, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("instance key: cannot read %s: %w", path, err)
	}
	defer f.Close()
	key, err := io.ReadAll(io.LimitReader(f, InstanceKeySize+1))
	if err != nil {
		return nil, fmt.Errorf("instance key: cannot read %s: %w", path, err)
	}
	if err := validKey(key); err != nil {
		return nil, fmt.Errorf("instance key: %s is malformed: %w", path, err)
	}
	return key, nil
}

func validKey(key []byte) error {
	if len(key) != InstanceKeySize {
		return fmt.Errorf("want exactly %d bytes", InstanceKeySize)
	}
	var acc byte
	for _, b := range key {
		acc |= b
	}
	if acc == 0 {
		return errors.New("key is all zero")
	}
	return nil
}

func createInstanceKey(path string) ([]byte, error) {
	key := make([]byte, InstanceKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := createPrivateFile(path)
	if err != nil {
		return nil, fmt.Errorf("instance key: cannot create %s: %w", path, err)
	}
	_, werr := f.Write(key)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(path) // created by us just now; a partial key must not survive
		return nil, fmt.Errorf("instance key: cannot write %s: %w", path, err)
	}
	// Re-read through the same checks used at every start.
	return loadInstanceKey(path)
}

// secretBox is AES-256-GCM under the instance key. A sealed value is
// version(1) || nonce(12) || ciphertext||tag and is bound to a purpose string (such as
// the owning user) through the additional data, so a ciphertext copied to another row
// does not open.
type secretBox struct{ aead cipher.AEAD }

const secretBoxVersion = 1

func newSecretBox(key []byte) (*secretBox, error) {
	if err := validKey(key); err != nil {
		return nil, fmt.Errorf("instance key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretBox{aead: aead}, nil
}

func (b *secretBox) seal(plain []byte, binding string) ([]byte, error) {
	n := b.aead.NonceSize()
	out := make([]byte, 1+n, 1+n+len(plain)+b.aead.Overhead())
	out[0] = secretBoxVersion
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, err
	}
	return b.aead.Seal(out, out[1:1+n], plain, []byte(binding)), nil
}

func (b *secretBox) open(sealed []byte, binding string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < 1+n+b.aead.Overhead() || sealed[0] != secretBoxVersion {
		return nil, errors.New("secretbox: malformed ciphertext")
	}
	plain, err := b.aead.Open(nil, sealed[1:1+n], sealed[1+n:], []byte(binding))
	if err != nil {
		return nil, errors.New("secretbox: authentication failed")
	}
	return plain, nil
}

// WithInstanceKey enables TOTP by supplying the instance key. An invalid key leaves
// TOTP unavailable (ErrNotConfigured) rather than weakly protected.
func WithInstanceKey(key []byte) Option {
	return func(s *Service) { s.box, _ = newSecretBox(key) }
}
