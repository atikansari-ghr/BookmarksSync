package floccus_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func TestCapabilitiesEnvelopeAdvertisesOnlySHA256AndJavascript(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	res := r.do(&a, "GET", "/ocs/v2.php/cloud/capabilities?format=json", nil)
	if res.status != 200 {
		t.Fatalf("status %d: %s", res.status, res.raw)
	}
	ocs, _ := res.body["ocs"].(map[string]any)
	meta, _ := ocs["meta"].(map[string]any)
	if meta["status"] != "ok" || num(meta["statuscode"]) != 200 {
		t.Errorf("meta = %v", meta)
	}
	caps := ocs["data"].(map[string]any)["capabilities"].(map[string]any)["bookmarks"].(map[string]any)
	if caps["javascript-bookmarks"] != true {
		t.Errorf("javascript-bookmarks = %v, want explicit true", caps["javascript-bookmarks"])
	}
	fns, _ := caps["hash-functions"].([]any)
	if len(fns) != 1 || fns[0] != "sha256" {
		t.Errorf("hash-functions = %v, want [sha256]", fns)
	}
	// the index.php form works too, and without redirects
	r.do(&a, "GET", "/index.php/ocs/v2.php/cloud/capabilities?format=json", nil)
}

func TestBothRouteBasesWorkWithoutRedirects(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	for _, base := range []string{"/index.php/apps/bookmarks/public/rest/v2", "/apps/bookmarks/public/rest/v2"} {
		res := r.do(&a, "GET", base+"/folder/-1/children?layers=1", nil).ok(t)
		if len(res.data()) != 2 {
			t.Errorf("%s: %s", base, res.raw)
		}
		// the bare base and odd slashes must not redirect either (status checked in send)
		r.do(&a, "GET", base, nil)
		r.do(&a, "GET", base+"/", nil)
		r.do(&a, "GET", base+"/folder/-1/children/", nil)
	}
}

func TestAuthenticationFailuresAreGeneric401(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	good := R + "/folder/-1/children?layers=1"

	noAuth := r.do(nil, "GET", good, nil).fail(t, 401)
	wrongSecret := a
	wrongSecret.secret = a.secret[:len(a.secret)-2] + "xx"
	unknownUser := a
	unknownUser.user.Username = "nobody"
	website := a
	website.secret = "website password alice"
	var bodies []string
	for name, acct := range map[string]account{"wrong secret": wrongSecret, "unknown user": unknownUser, "website password": website} {
		res := r.do(&acct, "GET", good, nil).fail(t, 401)
		bodies = append(bodies, res.raw)
		if strings.Contains(res.raw, "alice") || strings.Contains(res.raw, "secret") {
			t.Errorf("%s: 401 body leaks detail: %s", name, res.raw)
		}
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] || b != noAuth.raw {
			t.Errorf("401 bodies differ, leaking which part failed:\n%s\n%s", bodies[0], b)
		}
	}
	// every route family requires auth, including unknown paths under the prefix
	for _, p := range []string{R + "/bookmark", R + "/lock", R + "/nope", "/ocs/v2.php/cloud/capabilities", "/apps/bookmarks/public/rest/v2/folder"} {
		r.do(nil, "GET", p, nil).fail(t, 401)
	}
	// bearer, cookie and malformed basic are not credentials
	req, _ := http.NewRequest("GET", r.ts.URL+good, nil)
	req.Header.Set("Authorization", "Bearer "+a.secret)
	req.AddCookie(&http.Cookie{Name: "session", Value: a.secret})
	res, _ := r.client().Do(req)
	if res.StatusCode != 401 {
		t.Errorf("bearer accepted: %d", res.StatusCode)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("no-colon-here")))
	res, _ = r.client().Do(req)
	if res.StatusCode != 401 {
		t.Errorf("colon-less basic accepted: %d", res.StatusCode)
	}
}

func TestNoQueryParameterTokenAcceptance(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	for _, q := range []string{"access_token", "token", "password", "app_password", "secret", "auth", "apikey"} {
		for _, user := range []string{"", "&user=alice&username=alice&login=alice"} {
			p := R + "/folder/-1/children?layers=1&" + q + "=" + a.secret + user
			r.do(nil, "GET", p, nil).fail(t, 401)
		}
	}
	// credentials in the URL userinfo form are not read from the query either
	r.do(nil, "GET", R+"/folder/-1/children?layers=1&u=alice:"+a.secret, nil).fail(t, 401)
}

func TestRevokedAndInactiveCredentialsFailImmediatelyOverHTTP(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	p := R + "/folder/-1/children?layers=1"
	r.do(&a, "GET", p, nil).ok(t)
	noErr(t, r.ids.SetActive(bg, "alice", false))
	r.do(&a, "GET", p, nil).fail(t, 401)
	// fix round 1: deactivation revoked the credential; reactivation needs a new one
	noErr(t, r.ids.SetActive(bg, "alice", true))
	r.do(&a, "GET", p, nil).fail(t, 401)
	b := r.account("alice", identity.ModeEdit)
	r.do(&b, "GET", p, nil).ok(t)
	noErr(t, r.ids.RevokeCredential(bg, b.cred.ID))
	r.do(&b, "GET", p, nil).fail(t, 401)
	// a second credential of the same user is unaffected by revoking one
	c := r.account("alice", identity.ModeEdit)
	d := r.account("alice", identity.ModeEdit)
	noErr(t, r.ids.RevokeCredential(bg, c.cred.ID))
	r.do(&d, "GET", p, nil).ok(t)
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequestLogNamesCredentialButNeverSecrets(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	r.do(&a, "GET", R+"/folder/-1/children?layers=1&url=https://private.example/secret-path", nil).ok(t)
	bad := a
	bad.secret = "bms1.1.wrongwrongwrongwrongwrongwrongwrongwrongwro"
	r.do(&bad, "GET", R+"/bookmark", nil)
	logs := r.logs.String()
	if !strings.Contains(logs, "credential=") || !strings.Contains(logs, "status=200") || !strings.Contains(logs, "status=401") {
		t.Errorf("request log lacks verb/path/credential/status:\n%s", logs)
	}
	for _, leak := range []string{a.secret, strings.Split(a.secret, ".")[2], "website password", "private.example", "secret-path", "Authorization"} {
		if strings.Contains(logs, leak) {
			t.Errorf("log leaks %q:\n%s", leak, logs)
		}
	}
	if strings.Contains(logs, bad.secret) {
		t.Error("log leaks the rejected secret")
	}
}

func TestRouteMethodAndNotFoundErrorsUseTheEnvelope(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	res := r.do(&a, "GET", R+"/nope", nil).fail(t, 404)
	if res.header.Get("Content-Type") == "" || !strings.Contains(res.header.Get("Content-Type"), "json") {
		t.Errorf("content-type %q", res.header.Get("Content-Type"))
	}
	m := r.do(&a, "PUT", R+"/lock", nil).fail(t, 405)
	if allow := m.header.Get("Allow"); !strings.Contains(allow, "POST") || !strings.Contains(allow, "DELETE") {
		t.Errorf("Allow = %q", allow)
	}
	r.do(&a, "DELETE", R+"/folder/-1/children", nil).fail(t, 405)
	r.do(&a, "PATCH", R+"/bookmark", nil).fail(t, 405)
	r.do(&a, "GET", R+"/folder/1/2/3/4", nil).fail(t, 404)
}

func TestMalformedIDsAreRejectedNotGuessed(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bad := []string{"abc", "1e3", "0x10", "1.0", "-5", "-0", "+1", "%20", "9999999999999999999999", "٣", "1;2", "null"}
	for _, id := range bad {
		for _, p := range []string{"/folder/" + id + "/children?layers=1", "/folder/" + id + "/hash?hashFn=sha256", "/bookmark/" + id} {
			res := r.do(&a, "GET", R+p, nil)
			if res.status != 400 && res.status != 404 {
				t.Errorf("GET %s -> %d %s", p, res.status, res.raw)
			}
			if res.body["status"] != "error" {
				t.Errorf("GET %s lacks error envelope: %s", p, res.raw)
			}
		}
	}
	// JSON id forms: numbers and numeric strings are accepted, anything else is a 400
	bar, _ := r.roots(a)
	r.do(&a, "POST", R+"/folder", `{"parent_folder":"`+idStr(bar)+`","title":"string id"}`).ok(t)
	r.do(&a, "POST", R+"/folder", `{"parent_folder":`+idStr(bar)+`,"title":"number id"}`).ok(t)
	r.do(&a, "POST", R+"/folder", `{"parent_folder":"-1","title":"minus one string"}`).ok(t)
	r.do(&a, "POST", R+"/folder", `{"parent_folder":-1,"title":"minus one number"}`).ok(t)
	for _, v := range []string{`"abc"`, `1.5`, `1e2`, `true`, `[1]`, `{}`, `"1 "`, `""`} {
		r.do(&a, "POST", R+"/folder", `{"parent_folder":`+v+`,"title":"x"}`).fail(t, 400)
	}
}

func TestMalformedJSONAndBodies(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	cases := map[string]string{
		"not json":      `{`,
		"empty":         ``,
		"array":         `[]`,
		"trailing data": `{"parent_folder":-1,"title":"x"} {"a":1}`,
		"title not str": `{"parent_folder":-1,"title":5}`,
		"title null":    `{"parent_folder":-1,"title":null}`,
	}
	for name, body := range cases {
		res := r.do(&a, "POST", R+"/folder", []byte(body))
		if res.status != 400 || res.body["status"] != "error" {
			t.Errorf("%s: %d %s", name, res.status, res.raw)
		}
	}
	// a body larger than the server cap is refused with an envelope, not buffered forever
	big := `{"parent_folder":` + idStr(bar) + `,"title":"` + strings.Repeat("x", 2<<20) + `"}`
	res := r.do(&a, "POST", R+"/folder", big)
	if res.status != 413 || res.body["status"] != "error" {
		t.Errorf("oversized body: %d %.100s", res.status, res.raw)
	}
	// nothing was created by all the failures
	d := r.do(&a, "GET", R+"/folder/-1/children?layers=-1", nil).ok(t).data()
	if len(d) != 2 || len(d[0].(map[string]any)["children"].([]any)) != 0 {
		t.Errorf("failed requests left data behind: %v", d)
	}
}

func TestQueryValueValidation(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	r.mkBookmark(a, "https://q.example/", "q", bar)
	for _, q := range []string{"page=abc&limit=10", "page=0&limit=0", "page=0&limit=-3", "page=-2&limit=10", "page=0&limit=x", "page=99999999999999999999&limit=1"} {
		r.do(&a, "GET", R+"/bookmark?"+q, nil).fail(t, 400)
	}
	// oversized limits are clamped, never unbounded
	r.do(&a, "GET", R+"/bookmark?page=0&limit=100000000", nil).ok(t)
	for _, q := range []string{"layers=abc", "layers=0", "layers=-2", "layers=99999"} {
		r.do(&a, "GET", R+"/folder/-1/children?"+q, nil).fail(t, 400)
	}
	r.do(&a, "GET", R+"/folder/-1/hash?hashFn=md5", nil).fail(t, 400)
	r.do(&a, "GET", R+"/folder/-1/hash?hashFn=murmur3a", nil).fail(t, 400)
	r.do(&a, "GET", R+"/folder/-1/hash?hashFn=sha256&fields[]=title&fields[]=url&fields[]=tags", nil).fail(t, 400)
	r.do(&a, "GET", R+"/folder?root=abc&layers=0", nil).fail(t, 400)
	r.do(&a, "GET", R+"/folder?root=-1&layers=abc", nil).fail(t, 400)
}
