package network

import (
	"errors"
	"net/netip"
	"os"
	"sync"
	"time"
)

// ReloadingCountryDatabase observes atomic publications into a mounted directory.
// A failed refresh keeps the last valid reader; an initially missing database
// denies public traffic until a valid database appears. Lookups check at most
// once per 30 seconds, without a background goroutine or a container restart.
type ReloadingCountryDatabase struct {
	mu        sync.Mutex
	path      string
	current   *CountryDatabase
	info      os.FileInfo
	nextCheck time.Time
	now       func() time.Time
	closed    bool
}

// NewReloadingCountryDatabase returns a usable reloader even when its first
// refresh fails. The caller should retain and close it while reporting the error.
func NewReloadingCountryDatabase(path string) (*ReloadingCountryDatabase, error) {
	db := &ReloadingCountryDatabase{path: path, now: time.Now}
	return db, db.Refresh()
}

func (db *ReloadingCountryDatabase) refreshLocked() error {
	if db.closed {
		return errors.New("country database is closed")
	}
	db.nextCheck = db.now().Add(30 * time.Second)
	info, err := os.Lstat(db.path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 64<<20 {
		return errors.New("invalid country database file")
	}
	if db.info != nil && os.SameFile(info, db.info) && info.Size() == db.info.Size() && info.ModTime().Equal(db.info.ModTime()) {
		return nil
	}
	candidate, err := OpenCountryDatabase(db.path)
	if err != nil {
		return err
	}
	previous := db.current
	db.current, db.info = candidate, info
	if previous != nil {
		_ = previous.Close()
	}
	return nil
}

// Refresh immediately checks the on-disk database. Only a validated reader can
// replace the active mapping. Publishers must replace files rather than write
// into a mapped file in place.
func (db *ReloadingCountryDatabase) Refresh() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.refreshLocked()
}

func (db *ReloadingCountryDatabase) LookupCountry(ip netip.Addr) (string, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return "", errors.New("country database is closed")
	}
	if !db.now().Before(db.nextCheck) {
		_ = db.refreshLocked()
	}
	if db.current == nil {
		return "", nil
	}
	return db.current.LookupCountry(ip)
}

func (db *ReloadingCountryDatabase) Available() bool {
	if db == nil {
		return false
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	return !db.closed && db.current != nil
}

func (db *ReloadingCountryDatabase) BuildTime() time.Time {
	if db == nil {
		return time.Time{}
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.current == nil || db.closed {
		return time.Time{}
	}
	return db.current.BuildTime()
}

func (db *ReloadingCountryDatabase) Close() error {
	if db == nil {
		return nil
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	db.closed = true
	if db.current != nil {
		return db.current.Close()
	}
	return nil
}
