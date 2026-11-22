package api_test

import (
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/api"
	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
	"gopkg.in/yaml.v3"
)

type specOperation struct {
	OperationID string                `yaml:"operationId"`
	Summary     string                `yaml:"summary"`
	Description string                `yaml:"description"`
	Security    []map[string][]string `yaml:"security"`
	SecuritySet bool                  `yaml:"-"`
	TokenScope  string                `yaml:"x-token-scope"`
	Admin       bool                  `yaml:"x-admin-required"`
	Recent      bool                  `yaml:"x-recent-auth"`
	Public      bool                  `yaml:"x-public"`
	Responses   map[string]struct {
		Ref     string `yaml:"$ref"`
		Content map[string]struct {
			Schema map[string]any `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"responses"`
	RequestBody *struct {
		Content map[string]struct {
			Schema map[string]any `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"requestBody"`
	Parameters []struct {
		Name        string         `yaml:"name"`
		Description string         `yaml:"description"`
		In          string         `yaml:"in"`
		Required    bool           `yaml:"required"`
		Schema      map[string]any `yaml:"schema"`
	} `yaml:"parameters"`
}

func (o *specOperation) UnmarshalYAML(node *yaml.Node) error {
	type plain specOperation
	if err := node.Decode((*plain)(o)); err != nil {
		return err
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "security" {
			o.SecuritySet = true
			break
		}
	}
	return nil
}

type openAPISpec struct {
	OpenAPI string `yaml:"openapi"`
	Info    struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
	Components struct {
		Schemas map[string]componentSchema `yaml:"schemas"`
	} `yaml:"components"`
	Paths map[string]map[string]specOperation `yaml:"paths"`
}

type componentSchema struct {
	Required             []string             `yaml:"required"`
	Properties           map[string]yaml.Node `yaml:"properties"`
	AdditionalProperties any                  `yaml:"additionalProperties"`
}

type routeRecorder struct{ routes map[string]bool }

func (r routeRecorder) Handle(pattern string, _ http.Handler) {
	parts := strings.SplitN(pattern, " ", 2)
	if len(parts) == 2 {
		r.routes[strings.TrimPrefix(parts[1], "/api/v1")+" "+strings.ToLower(parts[0])] = true
	}
}

func TestOpenAPIRouteAndAuthorizationParity(t *testing.T) {
	e := newTEnv(t)
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.1.0" || spec.Info.Version != api.APIVersion {
		t.Fatalf("OpenAPI version %q, API version %q", spec.OpenAPI, spec.Info.Version)
	}
	registered := map[string]bool{}
	for _, route := range e.api.Routes() {
		path := strings.TrimPrefix(route.Path, "/api/v1")
		registered[path+" "+strings.ToLower(route.Method)] = true
	}
	rp, err := identity.NewRelyingParty(origin)
	if err != nil {
		t.Fatal(err)
	}
	factors := identity.New(e.db.SQL(), identity.WithRelyingParty(rp))
	authH, err := auth.New(auth.Options{Identity: e.ids, Factors: factors, PublicOrigin: origin})
	if err != nil {
		t.Fatal(err)
	}
	noOIDC := routeRecorder{routes: map[string]bool{}}
	authH.Register(noOIDC)
	for route := range noOIDC.routes {
		if strings.HasPrefix(route, "/auth/oidc/") {
			t.Errorf("OIDC route %q registered without a configured provider", route)
		}
	}
	configuredAuth, err := auth.New(auth.Options{Identity: e.ids, Factors: factors, PublicOrigin: origin,
		OIDC: auth.OIDCOptions{Providers: []auth.OIDCProvider{{ID: "test-provider", Name: "Test Provider", Issuer: "https://issuer.example", ClientID: "test-client", ClientSecret: "test-secret"}}}})
	if err != nil {
		t.Fatal(err)
	}
	recorder := routeRecorder{routes: map[string]bool{}}
	configuredAuth.Register(recorder)
	e.api.Register(recorder)
	recorder.Handle("GET /api/v1/version", nil)
	for _, route := range e.api.Routes() {
		path := strings.TrimPrefix(route.Path, "/api/v1")
		operation, ok := spec.Paths[path][strings.ToLower(route.Method)]
		if !ok {
			t.Errorf("OpenAPI is missing %s %s", route.Method, path)
			continue
		}
		if strings.TrimSpace(operation.Summary) == "" {
			t.Errorf("%s %s has no operation summary", route.Method, path)
		}
		if response, ok := operation.Responses["default"]; !ok || response.Ref != "#/components/responses/Error" {
			t.Errorf("%s %s has no shared default error response", route.Method, path)
		}
		if operation.Admin != route.Admin || operation.Recent != route.Recent {
			t.Errorf("%s %s auth metadata admin=%v/recent=%v, want %v/%v", route.Method, path, operation.Admin, operation.Recent, route.Admin, route.Recent)
		}
		if route.TokenScope != "" {
			if operation.TokenScope != string(route.TokenScope) {
				t.Errorf("%s %s token scope %q, want %q", route.Method, path, operation.TokenScope, route.TokenScope)
			}
			if !hasSecurity(operation, "sessionCookie") || !hasSecurity(operation, "personalToken") {
				t.Errorf("%s %s must document session and bearer security", route.Method, path)
			}
		} else if route.AnyToken {
			if !hasSecurity(operation, "sessionCookie") || !hasSecurity(operation, "personalToken") {
				t.Errorf("%s %s must document session and bearer security", route.Method, path)
			}
		} else if route.Public {
			if !operation.SecuritySet || len(operation.Security) != 0 || !operation.Public {
				t.Errorf("%s %s must be explicitly documented as public", route.Method, path)
			}
		} else if !hasSecurity(operation, "sessionCookie") || hasSecurity(operation, "personalToken") {
			t.Errorf("%s %s has incorrect session-only security", route.Method, path)
		}
	}
	for path, methods := range spec.Paths {
		for method := range methods {
			if !recorder.routes[path+" "+method] {
				t.Errorf("OpenAPI documents unregistered route %s %s", strings.ToUpper(method), path)
			}
		}
		for method, op := range methods {
			if !op.SecuritySet {
				t.Errorf("OpenAPI %s %s omits explicit security policy", strings.ToUpper(method), path)
			}
			if response, ok := op.Responses["default"]; !ok || response.Ref != "#/components/responses/Error" {
				t.Errorf("OpenAPI %s %s omits shared default error response", strings.ToUpper(method), path)
			}
		}
	}
	for route := range recorder.routes {
		parts := strings.SplitN(route, " ", 2)
		if len(parts) != 2 {
			t.Errorf("registered route %q is missing from OpenAPI", route)
			continue
		}
		if _, ok := spec.Paths[parts[0]][parts[1]]; !ok {
			t.Errorf("registered route %q is missing from OpenAPI", route)
		}
	}
	for _, key := range []string{
		"get /bookmarks", "post /bookmarks", "get /bookmarks/{id}",
		"patch /bookmarks/{id}", "delete /bookmarks/{id}",
		"delete /folders/{folderId}/bookmarks/{id}",
		"get /folders", "post /folders", "get /folders/{id}",
		"patch /folders/{id}", "delete /folders/{id}", "put /folders/{id}/order",
		"get /tags", "get /export", "post /import", "get /duplicates",
		"put /bookmarks/{id}/favorite", "delete /bookmarks/{id}/favorite",
		"post /bookmarks/bulk-move",
	} {
		parts := strings.SplitN(key, " ", 2)
		operation := spec.Paths[parts[1]][parts[0]]
		found := false
		for _, parameter := range operation.Parameters {
			if parameter.Name == "share" && parameter.In == "query" && !parameter.Required &&
				parameter.Schema["type"] == "integer" && parameter.Schema["format"] == "int64" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not document its share query parameter", key)
		}
	}
}

// TestOpenAPIOperationsDocumentSuccessContracts catches an OpenAPI spec that
// lists routes and auth metadata but cannot describe successful client calls.
func TestOpenAPIOperationsDocumentSuccessContracts(t *testing.T) {
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	// These handlers decode a request payload. This explicit list catches an
	// accidental omission of requestBody while allowing bodyless POST actions.
	bodyOperations := map[string]bool{
		"passwordLogin": true, "changePassword": true, "updateSettings": true,
		"createToken": true, "createConnection": true, "createBookmark": true,
		"updateBookmark": true, "createFolder": true, "updateFolder": true,
		"reorderFolderChildren": true, "grantShare": true, "updateShare": true,
		"adminCreateUser": true, "adminUpdateUser": true, "reauthenticate": true,
		"adminPatchSettings": true, "adminUpdateNetworkAccess": true, "adminPatchUserSettings": true,
		"completeSecondFactorLogin": true, "confirmTotpEnrollment": true,
		"disableTotp": true, "regenerateRecoveryCodes": true,
		"finishPasskeyLogin": true, "finishPasskeyRegistration": true,
		"renamePasskey": true, "adminResetPassword": true, "adminResetFactors": true,
		"importBookmarks": true, "moveBookmarks": true, "submitLinkCheck": true,
		"adminUploadBrandingAsset":  true,
		"adminUpdateBackupSchedule": true, "adminCreateBackupDestination": true,
		"adminUpdateBackupDestination": true, "adminTestBackupDestination": true,
	}
	// Literal status expectations are derived from the real handlers. Login
	// intentionally has both outcomes because TOTP is configured per account.
	wantStatuses := map[string][]string{
		"get /version":      {"200"},
		"post /auth/login":  {"200", "202"},
		"post /auth/logout": {"204"},
		"get /auth/session": {"200"}, "get /auth/csrf": {"200"},
		"get /auth/sessions": {"200"}, "delete /auth/sessions/{id}": {"204"},
		"post /auth/reauthenticate":      {"200"},
		"post /auth/login/second-factor": {"200"},
		"get /auth/totp":                 {"200"}, "post /auth/totp/enroll": {"200"},
		"post /auth/totp/confirm": {"200"}, "post /auth/totp/disable": {"200"},
		"post /auth/recovery-codes":      {"200"},
		"post /auth/passkey/login/begin": {"200"}, "post /auth/passkey/login/finish": {"200"},
		"get /auth/passkeys": {"200"}, "post /auth/passkeys/register/begin": {"200"},
		"post /auth/passkeys/register/finish": {"201"},
		"patch /auth/passkeys/{id}":           {"204"}, "delete /auth/passkeys/{id}": {"200"},
		"get /auth/oidc/providers": {"200"}, "post /auth/oidc/{provider}/login": {"200"},
		"post /auth/oidc/{provider}/link": {"200"}, "get /auth/oidc/{provider}/callback": {"303"},
		"get /auth/oidc/identities": {"200"}, "delete /auth/oidc/identities/{id}": {"200"},
		"get /me": {"200"}, "patch /me/password": {"200"},
		"get /me/settings": {"200"}, "patch /me/settings": {"200"}, "get /me/dashboard": {"200"},
		"get /me/tokens": {"200"}, "post /me/tokens": {"201"},
		"delete /me/tokens/{id}": {"204"}, "get /me/connections": {"200"},
		"post /me/connections": {"201"}, "delete /me/connections/{id}": {"204"},
		"get /bookmarks": {"200"}, "post /bookmarks": {"201"},
		"get /bookmarks/{id}": {"200"}, "patch /bookmarks/{id}": {"200"},
		"post /bookmarks/bulk-move": {"204"},
		"delete /bookmarks/{id}":    {"204"}, "delete /folders/{folderId}/bookmarks/{id}": {"204"},
		"put /bookmarks/{id}/favorite": {"204"}, "delete /bookmarks/{id}/favorite": {"204"},
		"get /folders": {"200"}, "post /folders": {"201"},
		"get /folders/{id}": {"200"}, "patch /folders/{id}": {"200"},
		"delete /folders/{id}": {"204"}, "put /folders/{id}/order": {"204"},
		"get /tags": {"200"}, "get /trash": {"200"}, "get /duplicates": {"200"},
		"post /link-checks": {"202"}, "get /link-checks/{id}": {"200"},
		"post /trash/{type}/{id}/restore": {"200"},
		"delete /trash/{type}/{id}":       {"204"}, "delete /trash": {"204"},
		"post /import": {"201"}, "get /export": {"200"},
		"get /shares/owned": {"200"}, "get /shares/incoming": {"200"},
		"post /shares": {"201"}, "patch /shares/{id}": {"200"}, "delete /shares/{id}": {"204"},
		"get /admin/users": {"200"}, "post /admin/users": {"201"},
		"patch /admin/users/{id}": {"200"}, "post /admin/users/{id}/password-reset": {"204"},
		"post /admin/users/{id}/factor-reset":        {"204"},
		"get /admin/users/{id}/connections":          {"200"},
		"delete /admin/users/{id}/connections/{cid}": {"204"}, "get /admin/system": {"200"}, "get /admin/health": {"200"}, "get /admin/activity": {"200"},
		"get /admin/settings": {"200"}, "patch /admin/settings": {"200"}, "get /admin/network-access": {"200"}, "patch /admin/network-access": {"200"}, "post /admin/settings/assets": {"201"},
		"get /admin/users/{id}/settings": {"200"}, "patch /admin/users/{id}/settings": {"200"},
		"get /admin/backups": {"200"}, "put /admin/backups/schedule": {"200"},
		"post /admin/backups/destinations": {"201"}, "put /admin/backups/destinations/{id}": {"200"},
		"delete /admin/backups/destinations/{id}": {"204"}, "post /admin/backups/destinations/test": {"200"}, "post /admin/backups/run": {"202"},
		"get /admin/backups/destinations/{id}/recovery-key": {"200"},
		"get /auth/presentation":                            {"200"}, "get /auth/presentation/assets/{id}": {"200"},
	}
	seenBodies := map[string]bool{}
	for path, methods := range spec.Paths {
		for method, operation := range methods {
			key := method + " " + path
			want, exists := wantStatuses[key]
			if !exists {
				t.Errorf("no expected success contract for documented operation %s", key)
			}
			hasSuccess := false
			seen := map[string]bool{}
			for status, response := range operation.Responses {
				code, err := strconv.Atoi(status)
				if err != nil || code < 200 || code >= 400 {
					continue
				}
				hasSuccess = true
				seen[status] = true
				if code == http.StatusNoContent || code == http.StatusSeeOther {
					continue
				}
				if len(response.Content) == 0 {
					t.Errorf("%s has success response %d without documented content", key, code)
				} else {
					for mediaType, content := range response.Content {
						if len(content.Schema) == 0 {
							t.Errorf("%s success response %d media type %s has no schema", key, code, mediaType)
						}
						if ref, ok := content.Schema["$ref"].(string); ok {
							assertOpenAPISchemaRef(t, spec.Components.Schemas, key, ref)
						}
					}
				}
			}
			if !hasSuccess {
				t.Errorf("%s has no documented success response", key)
			}
			for _, status := range want {
				if !seen[status] {
					t.Errorf("%s is missing actual success response %s", key, status)
				}
			}
			for status := range seen {
				found := false
				for _, expected := range want {
					found = found || status == expected
				}
				if !found {
					t.Errorf("%s documents unexpected success response %s", key, status)
				}
			}
			if bodyOperations[operation.OperationID] {
				seenBodies[operation.OperationID] = true
				if operation.RequestBody == nil || len(operation.RequestBody.Content) == 0 {
					t.Errorf("%s has no documented request body", key)
				} else {
					for mediaType, content := range operation.RequestBody.Content {
						if len(content.Schema) == 0 {
							t.Errorf("%s request media type %s has no schema", key, mediaType)
						}
						if ref, ok := content.Schema["$ref"].(string); ok {
							assertOpenAPISchemaRef(t, spec.Components.Schemas, key+" request "+mediaType, ref)
						}
					}
				}
			} else if operation.RequestBody != nil {
				t.Errorf("%s documents a request body but its handler does not consume one", key)
			}
		}
	}
	for operationID := range bodyOperations {
		if !seenBodies[operationID] {
			t.Errorf("request-body contract for %s is not attached to a registered operation", operationID)
		}
	}
	for key := range wantStatuses {
		parts := strings.SplitN(key, " ", 2)
		if _, ok := spec.Paths[parts[1]][parts[0]]; !ok {
			t.Errorf("expected success contract %s is not attached to a documented operation", key)
		}
	}
}

func TestOpenAPIClientSchemasIncludeCriticalFields(t *testing.T) {
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"VersionResponse":      {"version", "commit", "build_date"},
		"AuthState":            {"user", "session", "recentAuthUntil", "csrfToken"},
		"SecondFactorRequired": {"status", "flowId", "methods", "expiresAt"},
		"Bookmark":             {"id", "url", "title", "tags", "folders", "favorite"},
		"DuplicateList":        {"count", "groups", "page", "limit", "hasMore"},
		"DashboardSummary":     {"totalBookmarks", "duplicateCount", "lastContactAt", "recentChanges"},
		"AdminHealth":          {"cpuPercent", "memory", "disk", "uptimeSeconds", "unavailable", "dbReady", "syncErrors"},
		"AdminActivity":        {"items"},
		"RecentChanges":        {"since", "windowDays", "counts", "items", "page", "limit", "hasMore"},
		"RecentChange":         {"type", "id", "title", "url", "at"},
		"DuplicateGroup":       {"url", "bookmarks"},
		"LinkCheckRequest":     {"bookmarkIds"},
		"LinkCheckJob":         {"id", "status", "total", "done", "createdAt", "startedAt", "finishedAt", "results"},
		"LinkCheckResult":      {"bookmarkId", "url", "status", "httpStatus", "detail", "redirects", "checkedAt"},
		"TokenCreated":         {"token", "secret"},
		"ConnectionCreated":    {"connection", "secret"},
	}
	for schemaName, properties := range want {
		schema, ok := spec.Components.Schemas[schemaName]
		if !ok {
			t.Errorf("missing component schema %s", schemaName)
			continue
		}
		for _, property := range properties {
			if _, ok := schema.Properties[property]; !ok {
				t.Errorf("schema %s omits contract field %s", schemaName, property)
			}
		}
	}
}

func TestOpenAPIContractsMatchHandlerEdgeCases(t *testing.T) {
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}

	if operation := spec.Paths["/auth/passkeys/{id}"]["delete"]; operation.RequestBody != nil {
		t.Error("DELETE /auth/passkeys/{id} must not require a request body")
	}
	if !schemaRequires(spec.Components.Schemas["FolderCreateRequest"], "parentId") {
		t.Error("FolderCreateRequest must require parentId because the handler has no zero-id default")
	}
	if !schemaAllowsRootAndPositive(spec.Components.Schemas["FolderCreateRequest"].Properties["parentId"]) {
		t.Error("FolderCreateRequest.parentId must allow the scope root (-1) or a positive folder id")
	}
	for operationID, pathParam := range map[string]string{
		"reorderFolderChildren":   "id",
		"removeBookmarkPlacement": "folderId",
	} {
		var operation specOperation
		for _, methods := range spec.Paths {
			for _, candidate := range methods {
				if candidate.OperationID == operationID {
					operation = candidate
				}
			}
		}
		found := false
		for _, parameter := range operation.Parameters {
			if parameter.In == "path" && parameter.Name == pathParam {
				found = true
				if !mapSchemaAllowsRootAndPositive(parameter.Schema) {
					t.Errorf("%s path parameter %s must allow the scope root (-1) or a positive id", operation.OperationID, parameter.Name)
				}
			}
		}
		if !found {
			t.Errorf("%s has no path parameter %s", operationID, pathParam)
		}
	}
	if schemaRequired(spec.Components.Schemas["ConnectionCreateRequest"], "label") {
		t.Error("ConnectionCreateRequest.label is optional in the handler")
	}
	for _, schemaName := range []string{"Connection", "ConnectionCreated"} {
		kind := findProperty(spec.Components.Schemas[schemaName], "kind")
		if kind == nil {
			if schemaName == "ConnectionCreated" {
				connection := findProperty(spec.Components.Schemas[schemaName], "connection")
				kind = yamlMappingValue(yamlMappingValue(connection, "properties"), "kind")
			}
		}
		if !yamlEnumIs(kind, "private", "share") {
			t.Errorf("%s.kind must enumerate the two credential kinds returned by the service", schemaName)
		}
	}
	for operationID, max := range map[string]float64{"listBookmarks": 200, "listTrash": 1000, "adminListUsers": 200} {
		var found bool
		for _, methods := range spec.Paths {
			for _, operation := range methods {
				if operation.OperationID != operationID {
					continue
				}
				for _, parameter := range operation.Parameters {
					if parameter.In == "query" && parameter.Name == "limit" {
						found = true
						if _, hasMaximum := parameter.Schema["maximum"]; hasMaximum {
							t.Errorf("%s.limit documents maximum %v although the handler caps rather than rejects", operationID, max)
						}
					}
				}
			}
		}
		if !found {
			t.Errorf("%s has no limit query parameter", operationID)
		}
	}
	for _, schemaName := range []string{"LoginRequest", "SecondFactorRequest", "ReauthenticateRequest", "TOTPCodeRequest", "TOTPProofRequest", "CeremonyRequest", "PasskeyRegistrationFinishRequest", "RenamePasskeyRequest"} {
		if spec.Components.Schemas[schemaName].AdditionalProperties == false {
			t.Errorf("%s rejects extra properties that the auth decoder accepts", schemaName)
		}
	}
	if schemaRequired(spec.Components.Schemas["PasskeyRegistrationFinishRequest"], "name") {
		t.Error("PasskeyRegistrationFinishRequest.name is optional and defaults to Passkey in the handler")
	}
	nameDescription := ""
	if name := findProperty(spec.Components.Schemas["PasskeyRegistrationFinishRequest"], "name"); name != nil {
		if description := yamlMappingValue(name, "description"); description != nil {
			nameDescription = description.Value
		}
	}
	if !strings.Contains(strings.ToLower(nameDescription), "passkey") {
		t.Error("PasskeyRegistrationFinishRequest.name should document the Passkey default")
	}
	trashLimit := findProperty(spec.Components.Schemas["TrashPage"], "limit")
	if maximum := yamlMappingValue(trashLimit, "maximum"); maximum == nil || maximum.Value != "1000" {
		t.Error("TrashPage.limit should document the effective maximum of 1000")
	}
	placement := spec.Paths["/folders/{folderId}/bookmarks/{id}"]["delete"]
	placementDescription := ""
	for _, parameter := range placement.Parameters {
		if parameter.In == "path" && parameter.Name == "folderId" {
			placementDescription = parameter.Description
		}
	}
	if !strings.Contains(strings.ToLower(placementDescription), "share") || !strings.Contains(strings.ToLower(placementDescription), "only") {
		t.Error("folderId=-1 must be documented as a shared-folder-root case only")
	}
	if _, hasShare := queryParameter(spec.Paths["/trash"]["get"], "share"); hasShare {
		t.Error("GET /trash must not advertise share because the handler rejects that query")
	}
	// Favorites: the response field is a required boolean, the list filter accepts only
	// true/false, and the PUT/DELETE toggles take no body and require bookmarks:write.
	if !schemaRequired(spec.Components.Schemas["Bookmark"], "favorite") {
		t.Error("Bookmark.favorite must be required because every bookmark response includes it")
	}
	if favorite := findProperty(spec.Components.Schemas["Bookmark"], "favorite"); favorite == nil ||
		yamlMappingValue(favorite, "type") == nil || yamlMappingValue(favorite, "type").Value != "boolean" {
		t.Error("Bookmark.favorite must be a boolean")
	}
	if schema, ok := queryParameter(spec.Paths["/bookmarks"]["get"], "favorite"); !ok || schema["type"] != "boolean" {
		t.Errorf("listBookmarks favorite query must be a boolean, got %v", schema)
	}
	for _, method := range []string{"put", "delete"} {
		operation, ok := spec.Paths["/bookmarks/{id}/favorite"][method]
		if !ok {
			t.Errorf("%s /bookmarks/{id}/favorite is not documented", method)
			continue
		}
		if operation.RequestBody != nil {
			t.Errorf("%s /bookmarks/{id}/favorite must not take a request body", method)
		}
		if operation.TokenScope != "bookmarks:write" {
			t.Errorf("%s /bookmarks/{id}/favorite token scope %q, want bookmarks:write", method, operation.TokenScope)
		}
		if !strings.Contains(strings.ToLower(operation.Description), "read-only") {
			t.Errorf("%s /bookmarks/{id}/favorite must document that read-only share recipients may use it", method)
		}
	}
}

func schemaRequires(schema componentSchema, name string) bool { return schemaRequired(schema, name) }
func schemaRequired(schema componentSchema, name string) bool {
	for _, required := range schema.Required {
		if required == name {
			return true
		}
	}
	return false
}

func findProperty(schema componentSchema, name string) *yaml.Node {
	if property, ok := schema.Properties[name]; ok {
		return &property
	}
	return nil
}

func yamlEnumIs(node *yaml.Node, values ...string) bool {
	if node == nil {
		return false
	}
	enum := yamlMappingValue(node, "enum")
	if enum == nil || enum.Kind != yaml.SequenceNode || len(enum.Content) != len(values) {
		return false
	}
	got := map[string]bool{}
	for _, value := range enum.Content {
		got[value.Value] = true
	}
	for _, value := range values {
		if !got[value] {
			return false
		}
	}
	return true
}

func schemaAllowsRootAndPositive(node yaml.Node) bool {
	clauses := yamlMappingValue(&node, "anyOf")
	if clauses == nil || clauses.Kind != yaml.SequenceNode {
		return false
	}
	root, positive := false, false
	for _, clause := range clauses.Content {
		constValue := yamlMappingValue(clause, "const")
		minimum := yamlMappingValue(clause, "minimum")
		root = root || (constValue != nil && constValue.Value == "-1")
		positive = positive || (minimum != nil && minimum.Value == "1")
	}
	return root && positive
}

func mapSchemaAllowsRootAndPositive(schema map[string]any) bool {
	clauses, ok := schema["anyOf"].([]any)
	if !ok {
		return false
	}
	root, positive := false, false
	for _, value := range clauses {
		clause, ok := value.(map[string]any)
		if !ok {
			continue
		}
		root = root || clause["const"] == -1
		if minimum, ok := clause["minimum"].(int); ok && minimum == 1 {
			positive = true
		}
		if minimum, ok := clause["minimum"].(int64); ok && minimum == 1 {
			positive = true
		}
		if minimum, ok := clause["minimum"].(float64); ok && minimum == 1 {
			positive = true
		}
	}
	return root && positive
}

func queryParameter(operation specOperation, name string) (map[string]any, bool) {
	for _, parameter := range operation.Parameters {
		if parameter.In == "query" && parameter.Name == name {
			return parameter.Schema, true
		}
	}
	return nil, false
}

func TestOpenAPIQueryParametersMatchImplementedFilters(t *testing.T) {
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"listBookmarks": {"page", "limit", "folder", "url", "q", "tag", "favorite", "share"},
		"listFolders":   {"parent", "depth", "share"},
		"listTrash":     {"page", "limit"}, "adminListUsers": {"page", "limit"},
		"listDuplicates": {"page", "limit", "share"}, "submitLinkCheck": {"share"}, "getLinkCheck": {"share"}, "getDashboard": {"page", "limit"},
		"importBookmarks": {"folder", "share"}, "exportBookmarks": {"folder", "format", "share"},
		"finishOidcFlow": {"state", "code", "error"},
	}
	seen := map[string]bool{}
	for path, methods := range spec.Paths {
		for method, operation := range methods {
			parameters, ok := want[operation.OperationID]
			if !ok {
				continue
			}
			seen[operation.OperationID] = true
			got := map[string]bool{}
			for _, parameter := range operation.Parameters {
				if parameter.In == "query" {
					got[parameter.Name] = true
				}
			}
			for _, name := range parameters {
				if !got[name] {
					t.Errorf("%s %s omits query parameter %q", strings.ToUpper(method), path, name)
				}
			}
		}
	}
	for operationID := range want {
		if !seen[operationID] {
			t.Errorf("query contract is not attached to a registered operation: %s", operationID)
		}
	}
}

func TestOpenAPIPathParametersAreDeclared(t *testing.T) {
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	root := document.Content[0]
	paths := yamlMappingValue(root, "paths")
	if paths == nil {
		t.Fatal("OpenAPI document has no paths mapping")
	}
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path, item := paths.Content[i].Value, paths.Content[i+1]
		pathVars := regexp.MustCompile(`\{([^}]+)\}`).FindAllStringSubmatch(path, -1)
		if len(pathVars) == 0 {
			continue
		}
		declared := map[string]bool{}
		collectPathParameters(declared, yamlMappingValue(item, "parameters"))
		for j := 0; j+1 < len(item.Content); j += 2 {
			method := item.Content[j].Value
			if method == "parameters" {
				continue
			}
			collectPathParameters(declared, yamlMappingValue(item.Content[j+1], "parameters"))
		}
		for _, match := range pathVars {
			if !declared[match[1]] {
				t.Errorf("path %s has no required in:path parameter for {%s}", path, match[1])
			}
		}
	}
}

func yamlMappingValue(mapping *yaml.Node, name string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == name {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func collectPathParameters(declared map[string]bool, parameters *yaml.Node) {
	if parameters == nil || parameters.Kind != yaml.SequenceNode {
		return
	}
	for _, parameter := range parameters.Content {
		name := yamlMappingValue(parameter, "name")
		location := yamlMappingValue(parameter, "in")
		required := yamlMappingValue(parameter, "required")
		if name != nil && location != nil && required != nil && location.Value == "path" && required.Value == "true" {
			declared[name.Value] = true
		}
	}
}

func TestOpenAPIComponentReferencesResolve(t *testing.T) {
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				if key.Value == "$ref" {
					switch {
					case strings.HasPrefix(value.Value, "#/components/schemas/"):
						assertOpenAPISchemaRef(t, spec.Components.Schemas, "OpenAPI document", value.Value)
					case value.Value == "#/components/responses/Error":
					default:
						t.Errorf("unsupported or unresolved OpenAPI reference %q", value.Value)
					}
				}
				visit(value)
			}
			return
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(&doc)
}

func assertOpenAPISchemaRef(t *testing.T, schemas map[string]componentSchema, operation, ref string) {
	t.Helper()
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		t.Errorf("%s uses unsupported schema reference %q", operation, ref)
		return
	}
	if _, ok := schemas[strings.TrimPrefix(ref, prefix)]; !ok {
		t.Errorf("%s references missing schema %q", operation, ref)
	}
}

func hasSecurity(op specOperation, name string) bool {
	for _, item := range op.Security {
		if _, ok := item[name]; ok {
			return true
		}
	}
	return false
}

// TestOpenAPIRecentAuthMatchesServerBehavior probes every session-authenticated
// operation in the spec with a signed-in but stale (older than the reauthentication
// window) session. x-recent-auth must be documented exactly where the server
// answers reauthentication_required, across the auth, factor, OIDC and API routes.
func TestOpenAPIRecentAuthMatchesServerBehavior(t *testing.T) {
	e := newTEnv(t)
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	rp, err := identity.NewRelyingParty(origin)
	if err != nil {
		t.Fatal(err)
	}
	ids := identity.New(e.db.SQL(), identity.WithPasswordCost(8*1024, 1, 1), identity.WithClock(e.clock.Now), identity.WithRelyingParty(rp))
	authH, err := auth.New(auth.Options{Identity: ids, Factors: ids, PublicOrigin: origin, Now: e.clock.Now,
		OIDC: auth.OIDCOptions{Store: ids, Providers: []auth.OIDCProvider{{ID: "test-provider", Name: "Test Provider",
			Issuer: "https://issuer.example", ClientID: "test-client", ClientSecret: "test-secret"}}}})
	if err != nil {
		t.Fatal(err)
	}
	e.h = server.NewHandler(server.Options{Version: version.Info{Version: "test"}, Ready: e.db, BodyLimit: api.BodyLimit,
		Register: func(r server.Registrar) { authH.Register(r); e.api.Register(r) }})

	params := regexp.MustCompile(`\{[^}]+\}`)
	probed := 0
	for path, methods := range spec.Paths {
		for method, op := range methods {
			if !hasSecurity(op, "sessionCookie") {
				continue
			}
			s := e.login(e.alice)
			e.clock.Advance(auth.ReauthWindow + time.Minute)
			target := params.ReplaceAllStringFunc(path, func(p string) string {
				if p == "{provider}" {
					return "test-provider"
				}
				if p == "{type}" {
					return "bookmark"
				}
				return "999999"
			})
			rec := e.do(strings.ToUpper(method), "/api/v1"+target, "{}", append(s.mods(), withContentType("application/json"))...)
			got := rec.Code == http.StatusForbidden && errCode(t, rec) == "reauthentication_required"
			if got != op.Recent {
				t.Errorf("%s %s: x-recent-auth=%v, but a stale session got %d %s", strings.ToUpper(method), path, op.Recent, rec.Code, rec.Body.String())
			}
			probed++
		}
	}
	if probed < 50 {
		t.Fatalf("probed only %d session operations", probed)
	}
}
