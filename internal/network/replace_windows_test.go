//go:build windows

package network

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicRenameKeepsMappedReaderAndReopensReplacement(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "GeoLite2-Country.mmdb")
	candidate := filepath.Join(root, "candidate.mmdb")
	for src, dst := range map[string]string{
		"testdata/GeoIP2-Country-Test.mmdb":   active,
		"testdata/GeoLite2-Country-Test.mmdb": candidate,
	} {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := OpenCountryDatabase(active)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := os.Rename(candidate, active); err != nil {
		t.Fatalf("atomic rename over mapped MMDB failed: %v", err)
	}
	if _, err := reader.LookupCountry(netip.MustParseAddr("81.2.69.142")); err != nil {
		t.Fatalf("existing reader stopped working after atomic rename: %v", err)
	}
	updated, err := OpenCountryDatabase(active)
	if err != nil {
		t.Fatalf("replacement MMDB could not be opened: %v", err)
	}
	defer updated.Close()
}

func TestOpenGeoLiteRejectsReparseParent(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "reparse")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	fixture, err := os.ReadFile("testdata/GeoLite2-Country-Test.mmdb")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "db.mmdb"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGeoLiteCountryDatabase(filepath.Join(link, "db.mmdb")); err == nil {
		t.Fatal("database reached through a reparse parent was accepted")
	}
}
