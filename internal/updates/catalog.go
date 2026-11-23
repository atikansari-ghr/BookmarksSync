// Package updates lists immutable-ready main-build releases for the admin UI.
// Installation remains the responsibility of the separately restricted host updater.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	repository  = "atikansari-ghr/BookmarksSync"
	defaultURL  = "https://api.github.com/repos/atikansari-ghr/BookmarksSync/releases?per_page=100"
	maxBodySize = 2 << 20
)

var buildTagPattern = regexp.MustCompile(`^build-([0-9a-f]{40})$`)

// Release contains only safe metadata needed by the administrator. Asset URLs
// and release descriptions are intentionally omitted; the installer resolves
// the selected tag itself and verifies every downloaded artifact.
type Release struct {
	Tag         string    `json:"tag"`
	Commit      string    `json:"commit"`
	PublishedAt time.Time `json:"publishedAt"`
	URL         string    `json:"url"`
}

type Catalog struct {
	InstalledCommit string    `json:"installedCommit,omitempty"`
	Releases        []Release `json:"releases"`
	Latest          *Release  `json:"latest,omitempty"`
	UpdateAvailable bool      `json:"updateAvailable"`
	ApplyAvailable  bool      `json:"applyAvailable"`
}

// CatalogClient reads the public repository release list. BaseURL is exposed
// for deterministic tests and must never come from a request parameter.
type CatalogClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func (c CatalogClient) timeout() time.Duration {
	if c.HTTPClient != nil && c.HTTPClient.Timeout > 0 {
		return c.HTTPClient.Timeout
	}
	return 10 * time.Second
}

func (c CatalogClient) List(ctx context.Context, installedCommit string) (Catalog, error) {
	url := c.BaseURL
	if url == "" {
		url = defaultURL
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: c.timeout()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Catalog{}, errors.New("release catalog request could not be created")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := client.Do(req)
	if err != nil {
		return Catalog{}, errors.New("release catalog is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Catalog{}, errors.New("release catalog is unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil || len(body) > maxBodySize {
		return Catalog{}, errors.New("release catalog response is invalid")
	}
	var raw []struct {
		Tag         string `json:"tag_name"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return Catalog{}, errors.New("release catalog response is invalid")
	}

	catalog := Catalog{InstalledCommit: installedCommit, Releases: make([]Release, 0, len(raw))}
	for _, r := range raw {
		match := buildTagPattern.FindStringSubmatch(r.Tag)
		if len(match) != 2 || r.Draft || !r.Prerelease || r.PublishedAt == "" {
			continue
		}
		assets := make(map[string]bool, len(r.Assets))
		for _, a := range r.Assets {
			assets[a.Name] = true
		}
		if !assets["bookmarksync-linux-amd64"] || !assets["BUILD_INFO.txt"] || !assets["SHA256SUMS"] {
			continue
		}
		publishedAt, err := time.Parse(time.RFC3339, r.PublishedAt)
		if err != nil {
			continue
		}
		catalog.Releases = append(catalog.Releases, Release{
			Tag: r.Tag, Commit: match[1], PublishedAt: publishedAt.UTC(),
			URL: "https://github.com/" + repository + "/releases/tag/" + r.Tag,
		})
	}
	sort.Slice(catalog.Releases, func(i, j int) bool {
		return catalog.Releases[i].PublishedAt.After(catalog.Releases[j].PublishedAt)
	})
	if len(catalog.Releases) > 10 {
		catalog.Releases = catalog.Releases[:10]
	}
	if len(catalog.Releases) > 0 {
		latest := catalog.Releases[0]
		catalog.Latest = &latest
		catalog.UpdateAvailable = !strings.EqualFold(latest.Commit, installedCommit)
	}
	return catalog, nil
}

// CatalogService caches the public release list to avoid consuming GitHub's
// unauthenticated API quota every time an admin opens or refreshes the page.
type CatalogService struct {
	client CatalogClient
	ttl    time.Duration
	now    func() time.Time
	mu     sync.Mutex
	items  []Release
	until  time.Time
}

func NewCatalogService(client CatalogClient) *CatalogService {
	return &CatalogService{client: client, ttl: 10 * time.Minute, now: time.Now}
}

func (s *CatalogService) List(ctx context.Context, installedCommit string) (Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !now.Before(s.until) {
		fresh, err := s.client.List(ctx, "")
		if err != nil {
			return Catalog{}, err
		}
		s.items = append([]Release(nil), fresh.Releases...)
		s.until = now.Add(s.ttl)
	}
	return catalogFor(s.items, installedCommit), nil
}

func catalogFor(releases []Release, installedCommit string) Catalog {
	catalog := Catalog{InstalledCommit: installedCommit, Releases: append([]Release(nil), releases...)}
	if len(catalog.Releases) > 0 {
		latest := catalog.Releases[0]
		catalog.Latest = &latest
		catalog.UpdateAvailable = !strings.EqualFold(latest.Commit, installedCommit)
	}
	return catalog
}

// ValidateApprovedTag is shared with the host updater boundary. It accepts no
// branch names, refs, shell metacharacters, or abbreviated commit identifiers.
func ValidateApprovedTag(tag string) error {
	if !buildTagPattern.MatchString(tag) {
		return fmt.Errorf("release tag must be build- followed by a full lowercase commit SHA")
	}
	return nil
}
