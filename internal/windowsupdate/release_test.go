package windowsupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubVerifierChecksPublishedReleaseChecksumsAndEveryAttestation(t *testing.T) {
	work := t.TempDir()
	var fixture string
	var commands [][]string
	verify := GitHubReleaseVerifier{GHPath: filepath.Join(work, "gh.exe"), WorkDir: work}
	verify.Run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if args[0] == "release" && args[1] == "view" {
			return []byte(`{"tagName":"` + validTag + `","isDraft":false,"isPrerelease":true,"assets":[{"name":"` + WindowsServerAsset + `","size":10},{"name":"` + WindowsUpdaterAsset + `","size":10},{"name":"` + BuildInfoAsset + `","size":10},{"name":"` + ChecksumsAsset + `","size":10}]}`), nil
		}
		if args[0] == "release" && args[1] == "download" {
			if fixture == "" {
				fixture = filepath.Join(work, "fixture")
				if err := os.Mkdir(fixture, 0700); err != nil {
					t.Fatal(err)
				}
				writeVerifiedReleaseFixture(t, fixture)
			}
			name := args[len(args)-1]
			body, err := os.ReadFile(filepath.Join(fixture, name))
			if err != nil {
				return nil, err
			}
			return nil, os.WriteFile(filepath.Join(args[6], name), body, 0600)
		}
		if args[0] == "attestation" {
			return nil, nil
		}
		return nil, ErrReleaseInvalid
	}
	got, err := verify.Verify(context.Background(), validTag)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != validTag || len(commands) != 9 {
		t.Fatalf("release=%#v; commands=%d", got, len(commands))
	}
	for _, args := range commands[5:] {
		if len(args) < 8 || args[0] != "attestation" || args[1] != "verify" || !containsArg(args, "--signer-workflow") || !containsArg(args, releaseRepository+"/.github/workflows/ci.yml") || !containsArg(args, "refs/heads/main") || !containsArg(args, strings.TrimPrefix(validTag, "build-")) {
			t.Fatalf("attestation command is not scoped to the publishing workflow: %v", args)
		}
	}
}

func TestGitHubVerifierRejectsIneligibleReleaseBeforeDownload(t *testing.T) {
	work := t.TempDir()
	var downloaded bool
	verify := GitHubReleaseVerifier{GHPath: filepath.Join(work, "gh.exe"), WorkDir: work, Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "release" && args[1] == "view" {
			return []byte(`{"tagName":"` + validTag + `","isDraft":false,"isPrerelease":false,"assets":[]}`), nil
		}
		if args[1] == "download" {
			downloaded = true
		}
		return nil, nil
	}}
	if _, err := verify.Verify(context.Background(), validTag); err == nil || downloaded {
		t.Fatalf("ineligible release error=%v downloaded=%v", err, downloaded)
	}
}

func TestGitHubVerifierRejectsAttestationFailureAndCleansStaging(t *testing.T) {
	work := t.TempDir()
	var fixture string
	verify := GitHubReleaseVerifier{GHPath: filepath.Join(work, "gh.exe"), WorkDir: work}
	verify.Run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "release" && args[1] == "view" {
			return []byte(`{"tagName":"` + validTag + `","isDraft":false,"isPrerelease":true,"assets":[{"name":"` + WindowsServerAsset + `","size":10},{"name":"` + WindowsUpdaterAsset + `","size":10},{"name":"` + BuildInfoAsset + `","size":10},{"name":"` + ChecksumsAsset + `","size":10}]}`), nil
		}
		if args[0] == "release" && args[1] == "download" {
			if fixture == "" {
				fixture = filepath.Join(work, "fixture")
				_ = os.Mkdir(fixture, 0700)
				writeVerifiedReleaseFixture(t, fixture)
			}
			name := args[len(args)-1]
			body, err := os.ReadFile(filepath.Join(fixture, name))
			if err != nil {
				return nil, err
			}
			return nil, os.WriteFile(filepath.Join(args[6], name), body, 0600)
		}
		if args[0] == "attestation" {
			return nil, errors.New("attestation unavailable")
		}
		return nil, nil
	}
	if _, err := verify.Verify(context.Background(), validTag); err == nil {
		t.Fatal("attestation failure accepted")
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "release-") {
			t.Fatal("failed staging directory was retained")
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestVerifyReleaseFilesRequiresExactBuildAndEveryDigest(t *testing.T) {
	dir := t.TempDir()
	writeVerifiedReleaseFixture(t, dir)
	release, err := VerifyReleaseFiles(dir, validTag)
	if err != nil {
		t.Fatal(err)
	}
	if release.Commit != strings.TrimPrefix(validTag, "build-") || !filepath.IsAbs(release.ServerExecutable) || !filepath.IsAbs(release.UpdaterExecutable) {
		t.Fatalf("verified release = %#v", release)
	}
	cases := []struct {
		name string
		file string
		body string
	}{
		{"server mismatch", WindowsServerAsset, "modified-server"},
		{"updater mismatch", WindowsUpdaterAsset, "modified-updater"},
		{"metadata mismatch", BuildInfoAsset, "commit=wrong\nbuild_date=2026-11-23T00:00:00Z\nversion=wrong\n"},
		{"extra checksum", ChecksumsAsset, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeVerifiedReleaseFixture(t, dir)
			if tc.file == ChecksumsAsset {
				f, err := os.OpenFile(filepath.Join(dir, ChecksumsAsset), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = f.WriteString(strings.Repeat("0", 64) + "  unexpected.exe\n")
				_ = f.Close()
			} else if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyReleaseFiles(dir, validTag); err == nil {
				t.Fatal("VerifyReleaseFiles() succeeded with tampered assets")
			}
		})
	}
}

func TestVerifyReleaseFilesRejectsInvalidTagsAndNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	writeVerifiedReleaseFixture(t, dir)
	if _, err := VerifyReleaseFiles(dir, "latest"); err == nil {
		t.Fatal("invalid tag accepted")
	}
	if _, err := VerifyReleaseFiles("relative", validTag); err == nil {
		t.Fatal("relative release directory accepted")
	}
	if err := os.Remove(filepath.Join(dir, WindowsServerAsset)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, WindowsUpdaterAsset), filepath.Join(dir, WindowsServerAsset)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := VerifyReleaseFiles(dir, validTag); err == nil {
		t.Fatal("symlinked binary accepted")
	}
}

func writeVerifiedReleaseFixture(t *testing.T, dir string) {
	t.Helper()
	commit := strings.TrimPrefix(validTag, "build-")
	bodies := map[string][]byte{
		WindowsServerAsset:  []byte("server executable fixture"),
		WindowsUpdaterAsset: []byte("updater executable fixture"),
		BuildInfoAsset:      []byte("commit=" + commit + "\nbuild_date=2026-11-23T00:00:00Z\nversion=0.0.0-" + commit[:12] + "\n"),
	}
	for name, body := range bodies {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var sums strings.Builder
	for _, name := range []string{WindowsServerAsset, WindowsUpdaterAsset, BuildInfoAsset} {
		digest := sha256.Sum256(bodies[name])
		sums.WriteString(hex.EncodeToString(digest[:]) + "  " + name + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, ChecksumsAsset), []byte(sums.String()), 0600); err != nil {
		t.Fatal(err)
	}
}
