package api_test

import (
	"net/http"
	"os"
	"regexp"
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
	Security    []map[string][]string `yaml:"security"`
	SecuritySet bool                  `yaml:"-"`
	TokenScope  string                `yaml:"x-token-scope"`
	Admin       bool                  `yaml:"x-admin-required"`
	Recent      bool                  `yaml:"x-recent-auth"`
	Responses   map[string]struct {
		Ref string `yaml:"$ref"`
	} `yaml:"responses"`
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
	Paths map[string]map[string]specOperation `yaml:"paths"`
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
