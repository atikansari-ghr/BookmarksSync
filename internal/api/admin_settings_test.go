package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/geolite"
)

func TestAdminPerUserDefaultsRequireAdminAndRespectOverrides(t *testing.T) {
	e := newTEnv(t)
	alice := e.login(e.alice)
	if rec := e.do("PATCH", "/api/v1/admin/users/1/settings", map[string]any{"ui.theme": "midnight-glass"}, alice.mods()...); rec.Code != 403 {
		t.Fatalf("non-admin default update = %d %s", rec.Code, rec.Body.String())
	}
	root := e.login(e.root)
	rec := e.do("PATCH", "/api/v1/admin/users/1/settings", map[string]any{"ui.theme": "midnight-glass"}, root.mods()...)
	if rec.Code != 200 {
		t.Fatalf("admin default update = %d %s", rec.Code, rec.Body.String())
	}
	effective := e.do("GET", "/api/v1/me/settings", nil, alice.mods()...)
	if effective.Code != 200 || decode(t, effective)["settings"].(map[string]any)["ui.theme"] != "midnight-glass" {
		t.Fatalf("default not effective: %d %s", effective.Code, effective.Body.String())
	}
	if rec := e.do("PATCH", "/api/v1/me/settings", map[string]any{"ui.theme": "warm-glass"}, alice.mods()...); rec.Code != 200 {
		t.Fatalf("user override = %d %s", rec.Code, rec.Body.String())
	}
	effective = e.do("GET", "/api/v1/me/settings", nil, alice.mods()...)
	if decode(t, effective)["settings"].(map[string]any)["ui.theme"] != "warm-glass" {
		t.Fatalf("admin default overrode user choice: %s", effective.Body.String())
	}
}

type fakeGeoLiteRunner struct {
	config     geolite.Config
	configured bool
	runs       int
}

func (f *fakeGeoLiteRunner) Status(context.Context) (geolite.Status, error) {
	return geolite.Status{Configured: f.configured, Enabled: f.config.Enabled, Days: f.config.Days, StartTime: f.config.StartTime, DatabaseAvailable: true, DatabaseBuildTime: timePtr(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)), LastResult: "updated"}, nil
}
func (f *fakeGeoLiteRunner) Configure(_ context.Context, c geolite.Config) error {
	f.config = c
	f.configured = true
	return nil
}
func (f *fakeGeoLiteRunner) Clear(context.Context) error {
	f.configured = false
	f.config.AccountID = ""
	f.config.LicenseKey = ""
	f.config.Enabled = false
	return nil
}
func (f *fakeGeoLiteRunner) Run(context.Context) (string, error) {
	f.runs++
	return "bookmarksync-geolite-abc123", nil
}
func timePtr(t time.Time) *time.Time { return &t }

func TestAdminGeoLiteSettingsRequireAdminRecentAuthAndNeverReturnCredentials(t *testing.T) {
	runner := &fakeGeoLiteRunner{}
	e := newTEnvWithGeoLite(t, runner)
	alice, root := e.login(e.alice), e.login(e.root)
	if got := e.do("GET", "/api/v1/admin/geolite", nil, alice.mods()...); got.Code != http.StatusForbidden {
		t.Fatalf("non-admin status=%d %s", got.Code, got.Body.String())
	}
	if got := e.do("PUT", "/api/v1/admin/geolite", map[string]any{"accountId": "123", "licenseKey": "bad-key!", "enabled": true, "days": []string{"MON", "THU"}, "startTime": "04:00"}, root.mods()...); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid credentials=%d %s", got.Code, got.Body.String())
	}
	secret := "private_key_123"
	configured := e.do("PUT", "/api/v1/admin/geolite", map[string]any{"accountId": "12345", "licenseKey": secret, "enabled": true, "days": []string{"MON", "THU"}, "startTime": "04:00"}, root.mods()...)
	if configured.Code != http.StatusOK || strings.Contains(configured.Body.String(), secret) || strings.Contains(configured.Body.String(), "12345") {
		t.Fatalf("configuration response leaked credentials: %d %s", configured.Code, configured.Body.String())
	}
	run := e.do("POST", "/api/v1/admin/geolite/run", nil, root.mods()...)
	if run.Code != http.StatusAccepted || runner.runs != 1 {
		t.Fatalf("run=%d %s", run.Code, run.Body.String())
	}
	if got := e.do("DELETE", "/api/v1/admin/geolite", nil, root.mods()...); got.Code != http.StatusOK {
		t.Fatalf("clear=%d %s", got.Code, got.Body.String())
	}
	activity := e.do("GET", "/api/v1/admin/activity", nil, root.mods()...)
	if strings.Contains(activity.Body.String(), secret) {
		t.Fatal("audit log included the credential")
	}
}

func TestAdminNetworkAccessPolicyControlsAndValidation(t *testing.T) {
	e := newTEnv(t)
	alice, root := e.login(e.alice), e.login(e.root)
	if rec := e.do("GET", "/api/v1/admin/network-access", nil, alice.mods()...); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin network policy read = %d %s", rec.Code, rec.Body.String())
	}
	initial := e.do("GET", "/api/v1/admin/network-access", nil, root.mods()...)
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"countries":["IN","SA"]`) {
		t.Fatalf("default network policy = %d %s", initial.Code, initial.Body.String())
	}
	if strings.Contains(initial.Body.String(), e.backupDataDir) || strings.Contains(initial.Body.String(), "127.0.0.1") {
		t.Fatal("network policy response exposed local filesystem or proxy configuration")
	}
	if rec := e.do("PATCH", "/api/v1/admin/network-access", map[string]any{"mode": "block", "countries": []string{}}, root.mods()...); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"countries":[]`) {
		t.Fatalf("empty block list = %d %s", rec.Code, rec.Body.String())
	}
	settings, err := e.ids.SystemSettings(t.Context())
	if err != nil || settings["network.geo-mode"] != "block" || settings["network.geo-countries"] != "" {
		t.Fatalf("stored policy = %v, %v", settings, err)
	}
	for _, body := range []map[string]any{{"mode": "permit", "countries": []string{"SA"}}, {"mode": "allow", "countries": []string{"ZZ"}}, {"mode": "allow", "countries": nil}, {"mode": "allow", "countries": []string{""}}, {"mode": "allow", "countries": []string{"IN,SA"}}, {"mode": "allow", "countries": []string{" in"}}} {
		if rec := e.do("PATCH", "/api/v1/admin/network-access", body, root.mods()...); rec.Code != http.StatusBadRequest {
			t.Errorf("invalid policy %#v = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	activity := e.do("GET", "/api/v1/admin/activity", nil, root.mods()...)
	if activity.Code != http.StatusOK || !strings.Contains(activity.Body.String(), "system_settings_updated") {
		t.Fatalf("network policy audit event missing: %d %s", activity.Code, activity.Body.String())
	}
}

func TestAdminUserListIncludesThemeDefaults(t *testing.T) {
	e := newTEnv(t)
	theme := "midnight-glass"
	if _, err := e.ids.UpdateUserSettingDefaults(t.Context(), e.alice.ID, map[string]*string{"ui.theme": &theme}); err != nil {
		t.Fatal(err)
	}
	root := e.login(e.root)
	rec := e.do("GET", "/api/v1/admin/users?limit=200", nil, root.mods()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin users = %d %s", rec.Code, rec.Body.String())
	}
	users := decode(t, rec)["users"].([]any)
	got := map[string]any{}
	for _, raw := range users {
		user := raw.(map[string]any)
		got[user["username"].(string)] = user["themeDefault"]
	}
	if got["alice"] != "midnight-glass" || got["bob"] != nil {
		t.Fatalf("theme defaults = %#v, want alice default and no bob override", got)
	}
}

func makeAssetBody(t *testing.T, kind, filename string, content []byte) (string, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("kind", kind); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreateFormFile("asset", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.String(), w.FormDataContentType()
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 20, G: 80, B: 140, A: 255})
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestBrandingAssetRejectsUnsupportedTypeAndOversize(t *testing.T) {
	e := newTEnv(t)
	root := e.login(e.root)
	bad, contentType := makeAssetBody(t, "logo", "bad.svg", []byte("<svg>bad</svg>"))
	if rec := e.do("POST", "/api/v1/admin/settings/assets", bad, append(root.mods(), withContentType(contentType))...); rec.Code != 400 {
		t.Fatalf("SVG upload = %d %s", rec.Code, rec.Body.String())
	}
	large, contentType := makeAssetBody(t, "background", "large.png", append(testPNG(t), bytes.Repeat([]byte{0}, (2<<20)+1)...))
	if rec := e.do("POST", "/api/v1/admin/settings/assets", large, append(root.mods(), withContentType(contentType))...); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload = %d %s", rec.Code, rec.Body.String())
	}
}

func TestBrandingAssetPathCannotEscapeDataDirectory(t *testing.T) {
	e := newTEnv(t)
	root := e.login(e.root)
	body, contentType := makeAssetBody(t, "logo", "../../outside.png", testPNG(t))
	rec := e.do("POST", "/api/v1/admin/settings/assets", body, append(root.mods(), withContentType(contentType))...)
	if rec.Code != 201 {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}
	data := decode(t, rec)
	assetURL, _ := data["url"].(string)
	if !strings.HasPrefix(assetURL, "/api/v1/auth/presentation/assets/") || strings.Contains(assetURL, "..") {
		t.Fatalf("unsafe generated URL %q", assetURL)
	}
	entries, err := os.ReadDir(e.assetDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("asset directory entries = %v, %v", entries, err)
	}
	resolved, err := filepath.Abs(filepath.Join(e.assetDir, entries[0].Name()))
	if err != nil || filepath.Dir(resolved) != filepath.Clean(e.assetDir) {
		t.Fatalf("asset escaped directory: %q %v", resolved, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.assetDir), "outside.png")); !os.IsNotExist(err) {
		t.Fatalf("attacker filename created an outside file: %v", err)
	}
	public := e.do("GET", assetURL, nil)
	if public.Code != 200 || public.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("public image = %d %q", public.Code, public.Header().Get("Content-Type"))
	}
	if public.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("image response missing nosniff protection: %v", public.Header())
	}
	presentation := e.do("GET", "/api/v1/auth/presentation", nil)
	if got := decode(t, presentation)["logoUrl"]; got != assetURL {
		t.Fatalf("presentation logo URL = %v, want %q", got, assetURL)
	}
}

func TestBrandingAssetsAreOnlyServedWhenReferenced(t *testing.T) {
	e := newTEnv(t)
	root := e.login(e.root)
	body, contentType := makeAssetBody(t, "logo", "logo.png", testPNG(t))
	rec := e.do("POST", "/api/v1/admin/settings/assets", body, append(root.mods(), withContentType(contentType))...)
	if rec.Code != 201 {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}
	url := decode(t, rec)["url"].(string)
	id := strings.TrimPrefix(url, "/api/v1/auth/presentation/assets/")
	for _, path := range []string{"/api/v1/auth/presentation/assets/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if got := e.do("GET", path, nil).Code; got != http.StatusNotFound {
			t.Errorf("GET %q = %d, want 404", path, got)
		}
	}
	if rec := e.do("PATCH", "/api/v1/admin/settings", map[string]any{"branding.logo-id": nil}, root.mods()...); rec.Code != http.StatusOK {
		t.Fatalf("clear logo setting = %d %s", rec.Code, rec.Body.String())
	}
	if got := e.do("GET", url, nil).Code; got != http.StatusNotFound {
		t.Fatalf("unreferenced image = %d, want 404", got)
	}
	if _, err := os.Stat(filepath.Join(e.assetDir, id+".png")); !os.IsNotExist(err) {
		t.Fatalf("cleared asset file still exists: %v", err)
	}
}

func TestAdminSettingsCannotReferenceMissingBrandingAsset(t *testing.T) {
	e := newTEnv(t)
	root := e.login(e.root)
	missing := "0123456789abcdef0123456789abcdef"
	rec := e.do("PATCH", "/api/v1/admin/settings", map[string]any{"branding.logo-id": missing}, root.mods()...)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("setting missing logo asset = %d %s, want 400", rec.Code, rec.Body.String())
	}
}

func TestReplacingBrandingSettingKeepsAssetReferencedByOtherSetting(t *testing.T) {
	e := newTEnv(t)
	root := e.login(e.root)
	body, contentType := makeAssetBody(t, "logo", "logo.png", testPNG(t))
	first := e.do("POST", "/api/v1/admin/settings/assets", body, append(root.mods(), withContentType(contentType))...)
	if first.Code != http.StatusCreated {
		t.Fatalf("first upload = %d %s", first.Code, first.Body.String())
	}
	oldURL := decode(t, first)["url"].(string)
	oldID := strings.TrimPrefix(oldURL, "/api/v1/auth/presentation/assets/")
	if rec := e.do("PATCH", "/api/v1/admin/settings", map[string]any{"branding.background-id": oldID}, root.mods()...); rec.Code != http.StatusOK {
		t.Fatalf("share asset between settings = %d %s", rec.Code, rec.Body.String())
	}
	secondBody, contentType := makeAssetBody(t, "logo", "new-logo.png", testPNG(t))
	second := e.do("POST", "/api/v1/admin/settings/assets", secondBody, append(root.mods(), withContentType(contentType))...)
	if second.Code != http.StatusCreated {
		t.Fatalf("replacement upload = %d %s", second.Code, second.Body.String())
	}
	if got := e.do("GET", oldURL, nil).Code; got != http.StatusOK {
		t.Fatalf("image still referenced as background = %d, want 200", got)
	}
}

func TestPublicPresentationOnlyReturnsSafeBranding(t *testing.T) {
	e := newTEnv(t)
	root := e.login(e.root)
	if rec := e.do("PATCH", "/api/v1/admin/settings", map[string]any{"branding.app-title": "My Private Server", "branding.primary-color": "#124488"}, root.mods()...); rec.Code != 200 {
		t.Fatalf("branding settings = %d %s", rec.Code, rec.Body.String())
	}
	missing := "0123456789abcdef0123456789abcdef"
	if _, err := e.ids.UpdateSystemSettings(t.Context(), map[string]*string{"branding.logo-id": &missing}); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/api/v1/auth/presentation", nil)
	if rec.Code != 200 {
		t.Fatalf("presentation = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["appTitle"] != "My Private Server" || body["primaryColor"] != "#124488" || body["version"] != "test" {
		t.Fatalf("presentation values = %v", body)
	}
	if _, exists := body["logoUrl"]; exists {
		t.Fatalf("presentation returned a URL for a missing branding asset: %v", body["logoUrl"])
	}
	for _, key := range []string{"cookie", "csrfToken", "credentials", "bookmarks", "secret", "databasePath"} {
		if _, exists := body[key]; exists {
			t.Errorf("unsafe field %q exposed", key)
		}
	}
	activity := e.do("GET", "/api/v1/admin/activity", nil, root.mods()...)
	if activity.Code != 200 || strings.Contains(activity.Body.String(), "My Private Server") || strings.Contains(activity.Body.String(), "#124488") || !strings.Contains(activity.Body.String(), "system_settings_updated") {
		t.Fatalf("activity is missing safe event metadata or leaked settings: %d %s", activity.Code, activity.Body.String())
	}
	for key, value := range map[string]string{"branding.primary-color": "url(javascript:alert(1))", "branding.app-title": "<script>bad</script>"} {
		if r := e.do("PATCH", "/api/v1/admin/settings", map[string]any{key: value}, root.mods()...); r.Code != 400 {
			t.Errorf("unsafe %s returned %d body=%s", key, r.Code, r.Body.String())
		}
	}
}
