package network

import (
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReloadingCountryDatabaseLoadsAfterMissingStartupAndKeepsLastValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Country.mmdb")
	db, err := NewReloadingCountryDatabase(path)
	if err == nil || db.Available() {
		t.Fatal("missing startup database must be unavailable")
	}
	defer db.Close()
	publish := func(source string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", source))
		if err != nil {
			t.Fatal(err)
		}
		staged := path + ".next"
		if err := os.WriteFile(staged, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(staged, path); err != nil {
			t.Fatal(err)
		}
	}
	publish("GeoLite2-Country-Test.mmdb")
	if err := db.Refresh(); err != nil || !db.Available() || db.BuildTime().IsZero() {
		t.Fatalf("first sidecar publication not loaded: %v", err)
	}
	want, err := db.LookupCountry(netip.MustParseAddr("81.2.69.160"))
	if err != nil || want == "" {
		t.Fatalf("fixture lookup: %q %v", want, err)
	}
	if err := os.WriteFile(path+".next", []byte("invalid download"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".next", path); err != nil {
		t.Fatal(err)
	}
	if err := db.Refresh(); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if got, err := db.LookupCountry(netip.MustParseAddr("81.2.69.160")); err != nil || got != want {
		t.Fatalf("last known good reader lost: %q %v", got, err)
	}
	publish("GeoIP2-Country-Test.mmdb")
	if err := db.Refresh(); err != nil {
		t.Fatal(err)
	}
	if db.current.DatabaseType() != "GeoIP2-Country" {
		t.Fatal("replacement reader not installed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := db.Refresh(); err == nil || !db.Available() {
		t.Fatal("missing replacement lost working reader")
	}
}

func TestReloadingCountryDatabaseRefreshesOnLookupAndClosesSafely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Country.mmdb")
	db, _ := NewReloadingCountryDatabase(path)
	// Advance the injected clock so a lookup can pick up the first download.
	db.now = func() time.Time { return time.Now().Add(time.Minute) }
	data, err := os.ReadFile(filepath.Join("testdata", "GeoLite2-Country-Test.mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if code, err := db.LookupCountry(netip.MustParseAddr("81.2.69.160")); err != nil || code == "" {
		t.Fatalf("automatic initial refresh: %q %v", code, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 30 {
				_, _ = db.LookupCountry(netip.MustParseAddr("81.2.69.160"))
				_ = db.BuildTime()
				_ = db.Refresh()
			}
		})
	}
	_ = db.Close()
	wg.Wait()
	if db.Available() {
		t.Fatal("closed reader is available")
	}
	if err := db.Refresh(); err == nil {
		t.Fatal("closed reader reopened")
	}
	if code, err := db.LookupCountry(netip.MustParseAddr("81.2.69.160")); err == nil || code != "" {
		t.Fatal("closed reader used")
	}
}
