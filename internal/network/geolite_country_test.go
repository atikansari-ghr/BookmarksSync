package network

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenGeoLiteCountryDatabaseRequiresExactEdition(t *testing.T) {
	valid, err := OpenGeoLiteCountryDatabase(filepath.Join("testdata", "GeoLite2-Country-Test.mmdb"))
	if err != nil {
		t.Fatalf("OpenGeoLiteCountryDatabase(GeoLite fixture): %v", err)
	}
	if valid.DatabaseType() != "GeoLite2-Country" {
		t.Fatalf("GeoLite database type = %q", valid.DatabaseType())
	}
	_ = valid.Close()

	if _, err := OpenGeoLiteCountryDatabase(filepath.Join("testdata", "GeoIP2-Country-Test.mmdb")); err == nil {
		t.Fatal("commercial GeoIP2 Country file accepted for a GeoLite update")
	}
	if _, err := OpenCountryDatabase(filepath.Join("testdata", "GeoIP2-Country-Test.mmdb")); err != nil {
		t.Fatalf("general country reader no longer accepts GeoIP2 Country: %v", err)
	}
}

func TestOpenGeoLiteCountryDatabaseRejectsOversizedAndNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	large := filepath.Join(dir, "large.mmdb")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64<<20 + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := OpenGeoLiteCountryDatabase(large); err == nil {
		t.Fatal("oversized GeoLite file accepted")
	}

	if _, err := OpenGeoLiteCountryDatabase(dir); err == nil {
		t.Fatal("directory accepted as a GeoLite database")
	}
	if _, err := OpenGeoLiteCountryDatabase(filepath.Join(dir, "missing.mmdb")); err == nil {
		t.Fatal("missing GeoLite database accepted")
	}
}
