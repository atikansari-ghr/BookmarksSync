package windowsupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/updates"
)

const (
	WindowsServerAsset  = "bookmarksync-windows-amd64.exe"
	WindowsUpdaterAsset = "bookmarksync-updater-windows-amd64.exe"
	BuildInfoAsset      = "BUILD_INFO.txt"
	ChecksumsAsset      = "SHA256SUMS"
)

var ErrReleaseInvalid = errors.New("Windows release verification failed")

const releaseRepository = "atikansari-ghr/BookmarksSync"

type GHCommand func(context.Context, string, ...string) ([]byte, error)

type GitHubReleaseVerifier struct {
	GHPath  string
	WorkDir string
	Run     GHCommand
}

func (v GitHubReleaseVerifier) Verify(ctx context.Context, tag string) (VerifiedRelease, error) {
	if updates.ValidateApprovedTag(tag) != nil || !filepath.IsAbs(v.GHPath) || !filepath.IsAbs(v.WorkDir) || rejectReparseAncestors(v.WorkDir) != nil {
		return VerifiedRelease{}, ErrReleaseInvalid
	}
	run := v.Run
	if run == nil {
		run = runGHCommand
	}
	metadata, err := run(ctx, v.GHPath, "release", "view", tag, "--repo", releaseRepository, "--json", "tagName,isDraft,isPrerelease,assets")
	if err != nil {
		return VerifiedRelease{}, ErrReleaseInvalid
	}
	var release struct {
		TagName    string `json:"tagName"`
		Draft      bool   `json:"isDraft"`
		Prerelease bool   `json:"isPrerelease"`
		Assets     []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if json.Unmarshal(metadata, &release) != nil || release.TagName != tag || release.Draft || !release.Prerelease {
		return VerifiedRelease{}, ErrReleaseInvalid
	}
	want := []string{WindowsServerAsset, WindowsUpdaterAsset, BuildInfoAsset, ChecksumsAsset}
	available := make(map[string]bool, len(release.Assets))
	for _, asset := range release.Assets {
		if available[asset.Name] || asset.Size < 1 || asset.Size > 512<<20 {
			return VerifiedRelease{}, ErrReleaseInvalid
		}
		available[asset.Name] = true
	}
	for _, name := range want {
		if !available[name] {
			return VerifiedRelease{}, ErrReleaseInvalid
		}
	}
	if err := os.MkdirAll(v.WorkDir, 0700); err != nil {
		return VerifiedRelease{}, ErrReleaseInvalid
	}
	if rejectReparseAncestors(v.WorkDir) != nil {
		return VerifiedRelease{}, ErrReleaseInvalid
	}
	dir, err := os.MkdirTemp(v.WorkDir, "release-"+strings.TrimPrefix(tag, "build-")[:12]+"-")
	if err != nil {
		return VerifiedRelease{}, ErrReleaseInvalid
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, name := range want {
		if _, err := run(ctx, v.GHPath, "release", "download", tag, "--repo", releaseRepository, "--dir", dir, "--pattern", name); err != nil {
			return VerifiedRelease{}, ErrReleaseInvalid
		}
	}
	verified, err := VerifyReleaseFiles(dir, tag)
	if err != nil {
		return VerifiedRelease{}, ErrReleaseInvalid
	}
	for _, asset := range want {
		file := filepath.Join(dir, asset)
		if _, err := run(ctx, v.GHPath, "attestation", "verify", file, "--repo", releaseRepository, "--signer-workflow", releaseRepository+"/.github/workflows/ci.yml", "--source-ref", "refs/heads/main", "--source-digest", strings.TrimPrefix(tag, "build-"), "--predicate-type", "https://slsa.dev/provenance/v1"); err != nil {
			return VerifiedRelease{}, ErrReleaseInvalid
		}
	}
	keep = true
	return verified, nil
}

func runGHCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, ErrReleaseInvalid
	}
	return output.Bytes(), nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	const max = 2 << 20
	n := len(p)
	remaining := max - b.Len()
	if remaining > 0 {
		if remaining > n {
			remaining = n
		}
		_, _ = b.Buffer.Write(p[:remaining])
	}
	return n, nil
}

func VerifyReleaseFiles(dir, tag string) (VerifiedRelease, error) {
	if updates.ValidateApprovedTag(tag) != nil || !filepath.IsAbs(dir) {
		return VerifiedRelease{}, ErrReleaseInvalid
	}
	commit := strings.TrimPrefix(tag, "build-")
	if err := verifyBuildInfo(filepath.Join(dir, BuildInfoAsset), commit); err != nil {
		return VerifiedRelease{}, ErrReleaseInvalid
	}
	checksums, err := parseChecksums(filepath.Join(dir, ChecksumsAsset))
	if err != nil {
		return VerifiedRelease{}, ErrReleaseInvalid
	}
	assets := []string{WindowsServerAsset, WindowsUpdaterAsset, BuildInfoAsset}
	for _, name := range assets {
		actual, err := digestRegularFile(filepath.Join(dir, name))
		if err != nil || checksums[name] != actual {
			return VerifiedRelease{}, ErrReleaseInvalid
		}
	}
	return VerifiedRelease{
		Tag: tag, Commit: commit,
		ServerExecutable:  filepath.Join(dir, WindowsServerAsset),
		UpdaterExecutable: filepath.Join(dir, WindowsUpdaterAsset),
		BuildInfo:         filepath.Join(dir, BuildInfoAsset), SHA256Sums: filepath.Join(dir, ChecksumsAsset),
	}, nil
}

func digestRegularFile(path string) (string, error) {
	return digestFileLimit(path, 512<<20)
}

func digestFileLimit(path string, maxSize int64) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxSize {
		return "", ErrReleaseInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ErrReleaseInvalid
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxSize+1)); err != nil {
		return "", ErrReleaseInvalid
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyBuildInfo(path, commit string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 4096 {
		return ErrReleaseInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return ErrReleaseInvalid
	}
	defer f.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(f, 4097))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok || key == "" || value == "" || values[key] != "" {
			return ErrReleaseInvalid
		}
		values[key] = value
	}
	_, dateErr := time.Parse(time.RFC3339, values["build_date"])
	if err := scanner.Err(); err != nil || dateErr != nil || len(values) != 3 || values["commit"] != commit || values["version"] != "0.0.0-"+commit[:12] {
		return ErrReleaseInvalid
	}
	return nil
}

func parseChecksums(path string) (map[string]string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 4096 {
		return nil, ErrReleaseInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrReleaseInvalid
	}
	defer f.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(f, 4097))
	for scanner.Scan() {
		digest, name, ok := strings.Cut(scanner.Text(), "  ")
		if !ok || len(digest) != 64 || filepath.Base(name) != name {
			return nil, ErrReleaseInvalid
		}
		if _, err := hex.DecodeString(digest); err != nil || values[name] != "" {
			return nil, ErrReleaseInvalid
		}
		if name != WindowsServerAsset && name != WindowsUpdaterAsset && name != BuildInfoAsset {
			return nil, ErrReleaseInvalid
		}
		values[name] = strings.ToLower(digest)
	}
	if err := scanner.Err(); err != nil || len(values) != 3 {
		return nil, ErrReleaseInvalid
	}
	return values, nil
}
