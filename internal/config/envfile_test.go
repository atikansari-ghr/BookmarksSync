package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnvironmentFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bookmarksync.env")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadEnvironmentFileLoadsValuesAndProcessEnvironmentWins(t *testing.T) {
	path := writeEnvironmentFile(t, "# operator settings\nBOOKMARKSYNC_PUBLIC_URL=https://saved.example\nBOOKMARKSYNC_OIDC_GOOGLE_CLIENT_SECRET=file-secret=with-equals\n")
	getenv, err := ReadEnvironmentFile(path, func(key string) string {
		if key == EnvPublicURL {
			return "https://process.example"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := getenv(EnvPublicURL); got != "https://process.example" {
		t.Fatalf("process environment did not override file: got %q", got)
	}
	if got := getenv("BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_SECRET"); got != "file-secret=with-equals" {
		t.Fatalf("file value was not preserved: got %q", got)
	}
	if got := getenv("BOOKMARKSYNC_NOT_CONFIGURED"); got != "" {
		t.Fatalf("unknown lookup returned %q", got)
	}
}

func TestReadEnvironmentFileRejectsMalformedDuplicateUnknownAndOversizeWithoutEchoingSecrets(t *testing.T) {
	secret := "do-not-echo-this-secret"
	cases := []struct {
		name    string
		content string
	}{
		{name: "missing equals", content: "BOOKMARKSYNC_PUBLIC_URL"},
		{name: "duplicate", content: "BOOKMARKSYNC_PUBLIC_URL=https://one.example\nBOOKMARKSYNC_PUBLIC_URL=" + secret},
		{name: "unknown key", content: "BOOKMARKSYNC_UNKNOWN=" + secret},
		{name: "non-bookmarksync key", content: "PATH=" + secret},
		{name: "nul byte", content: "BOOKMARKSYNC_PUBLIC_URL=https://one.example\x00"},
		{name: "invalid UTF-8", content: string([]byte{'B', 'O', 'O', 'K', 'M', 'A', 'R', 'K', 'S', 'Y', 'N', 'C', '_', 'L', 'I', 'S', 'T', 'E', 'N', '=', 0xff})},
		{name: "long line", content: "BOOKMARKSYNC_PUBLIC_URL=" + strings.Repeat("x", maxEnvironmentLineSize)},
		{name: "oversize", content: "BOOKMARKSYNC_PUBLIC_URL=" + strings.Repeat("x", maxEnvironmentFileSize)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeEnvironmentFile(t, tc.content)
			_, err := ReadEnvironmentFile(path, func(string) string { return "" })
			if err == nil {
				t.Fatal("ReadEnvironmentFile() succeeded; want validation error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error disclosed a configured secret: %v", err)
			}
		})
	}
}

func TestReadEnvironmentFileRejectsUnreadableOrNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadEnvironmentFile(dir, func(string) string { return "" }); err == nil {
		t.Fatal("ReadEnvironmentFile(directory) succeeded; want error")
	}
}
