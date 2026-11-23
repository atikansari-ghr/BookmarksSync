package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	maxEnvironmentFileSize = 64 << 10
	maxEnvironmentLineSize = 8 << 10
)

var allowedEnvironmentKeys = map[string]struct{}{
	EnvListen: {}, EnvDataDir: {}, EnvPublicURL: {}, EnvTrustedProxies: {}, EnvManagementCIDRs: {}, EnvGeoIPDB: {},
	"BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_ID": {}, "BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_SECRET": {},
	"BOOKMARKSYNC_OIDC_AUTHENTIK_ISSUER": {}, "BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_ID": {}, "BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_SECRET": {},
	"BOOKMARKSYNC_RCLONE_BINARY": {}, "BOOKMARKSYNC_RCLONE_CONFIG": {}, "BOOKMARKSYNC_UPDATE_SERVICE_USER": {},
}

// ReadEnvironmentFile loads literal KEY=value lines from a protected service
// configuration file. Non-empty process environment values take precedence.
// The format is deliberately smaller than a shell file: it has no quoting,
// interpolation, command substitution, or export statements.
func ReadEnvironmentFile(path string, inherited func(string) string) (func(string) string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("environment file path is required")
	}
	if inherited == nil {
		inherited = os.Getenv
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect service environment file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("service environment file must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open service environment file: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, errors.New("service environment file changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxEnvironmentFileSize+1))
	if err != nil {
		return nil, errors.New("read service environment file")
	}
	if len(body) > maxEnvironmentFileSize {
		return nil, errors.New("service environment file is too large")
	}
	if !utf8.Valid(body) {
		return nil, errors.New("service environment file must be UTF-8")
	}
	if strings.IndexByte(string(body), 0) >= 0 {
		return nil, errors.New("service environment file contains a NUL byte")
	}

	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 1024), maxEnvironmentLineSize)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid service environment entry on line %d", lineNumber)
		}
		if _, ok := allowedEnvironmentKeys[key]; !ok {
			return nil, fmt.Errorf("unsupported service environment key on line %d", lineNumber)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("duplicate service environment key on line %d", lineNumber)
		}
		values[key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, errors.New("service environment line is too long")
		}
		return nil, errors.New("read service environment file")
	}
	return func(key string) string {
		if value := inherited(key); value != "" {
			return value
		}
		return values[key]
	}, nil
}
