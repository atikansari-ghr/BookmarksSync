package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

const pw = "a long enough password"

type cli struct {
	t   *testing.T
	dir string
}

func newCLI(t *testing.T) *cli { return &cli{t: t, dir: filepath.Join(t.TempDir(), "data")} }

func (c *cli) run(stdin string, args ...string) (int, string, string) {
	c.t.Helper()
	var out, errb bytes.Buffer
	args = append(args, "--data-dir", c.dir)
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errb, func(string) string { return "" })
	return code, out.String(), errb.String()
}

func (c *cli) mustRun(stdin string, args ...string) (string, string) {
	c.t.Helper()
	code, out, errb := c.run(stdin, args...)
	if code != 0 {
		c.t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errb)
	}
	return out, errb
}

func (c *cli) db() *database.DB {
	c.t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(c.dir, "bookmarksync.db"))
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { db.Close() })
	return db
}

func TestHelpIsClearForEveryCommand(t *testing.T) {
	for _, args := range [][]string{
		{"--help"}, {"-h"}, {"help"},
		{"serve", "--help"}, {"user", "--help"}, {"user", "create", "--help"}, {"user", "list", "--help"}, {"user", "set-active", "--help"},
		{"credential", "--help"}, {"credential", "create", "--help"}, {"credential", "revoke", "--help"}, {"credential", "list", "--help"},
		{"share", "--help"}, {"share", "grant", "--help"}, {"share", "revoke", "--help"}, {"share", "list", "--help"},
		{"network", "--help"}, {"network", "reset-geo", "--help"},
	} {
		var out, errb bytes.Buffer
		code := run(context.Background(), args, strings.NewReader(""), &out, &errb, func(string) string { return "" })
		if code != 0 {
			t.Errorf("%v: exit %d, stderr %q", args, code, errb.String())
			continue
		}
		if !strings.Contains(out.String(), "usage") && !strings.Contains(out.String(), "Usage") {
			t.Errorf("%v: help lacks a usage line: %q", args, out.String())
		}
	}
	var out bytes.Buffer
	run(context.Background(), []string{"--help"}, strings.NewReader(""), &out, &bytes.Buffer{}, func(string) string { return "" })
	for _, want := range []string{"serve", "user create", "credential create", "credential revoke", "share grant", "network reset-geo", "--password-stdin"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("top-level help does not mention %q:\n%s", want, out.String())
		}
	}
	var cred bytes.Buffer
	run(context.Background(), []string{"credential", "create", "--help"}, strings.NewReader(""), &cred, &bytes.Buffer{}, func(string) string { return "" })
	if !strings.Contains(cred.String(), "once") || !strings.Contains(cred.String(), "--mode") {
		t.Errorf("credential create help must explain the one-time secret and --mode:\n%s", cred.String())
	}
}

func TestNetworkResetGeoRestoresDefaultsAndAuditsRecovery(t *testing.T) {
	c := newCLI(t)
	db := c.db()
	if _, err := db.SQL().Exec(`INSERT INTO system_settings(key,value,updated_at) VALUES('network.geo-mode','block',1),('network.geo-countries','US',1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	out, _ := c.mustRun("", "network", "reset-geo")
	if !strings.Contains(out, "reset to allow IN,SA") {
		t.Fatalf("reset output = %q", out)
	}
	check := c.db()
	defer check.Close()
	var mode, countries string
	if err := check.SQL().QueryRow(`SELECT value FROM system_settings WHERE key='network.geo-mode'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := check.SQL().QueryRow(`SELECT value FROM system_settings WHERE key='network.geo-countries'`).Scan(&countries); err != nil {
		t.Fatal(err)
	}
	var actor any
	var action string
	if err := check.SQL().QueryRow(`SELECT actor_id,action FROM admin_activity ORDER BY id DESC LIMIT 1`).Scan(&actor, &action); err != nil {
		t.Fatal(err)
	}
	if mode != "allow" || countries != "IN,SA" || actor != nil || action != "system_settings_updated" {
		t.Fatalf("reset state = mode %q countries %q audit (%v,%q)", mode, countries, actor, action)
	}
}

func TestUnknownSubcommandsAndFlagsAreUsageErrors(t *testing.T) {
	c := newCLI(t)
	for _, args := range [][]string{{"user"}, {"user", "frob"}, {"credential", "nope"}, {"share"}, {"user", "create"}, {"credential", "create"}} {
		if code, _, errb := c.run("", args...); code != 2 || errb == "" {
			t.Errorf("%v: exit %d stderr %q, want usage error 2", args, code, errb)
		}
	}
}

func TestUserCreateUsesStdinOnlyAndNeverEchoesThePassword(t *testing.T) {
	c := newCLI(t)
	// a password argument is not an option at all
	for _, args := range [][]string{
		{"user", "create", "--username", "alice", "--password", pw},
		{"user", "create", "--username", "alice", "--password=" + pw},
		{"user", "create", "--username", "alice", pw},
	} {
		code, out, errb := c.run(pw+"\n", args...)
		if code == 0 {
			t.Errorf("%v accepted a plaintext password argument", args)
		}
		if strings.Contains(out+errb, pw) {
			t.Errorf("%v echoed the password:\nstdout %s\nstderr %s", args, out, errb)
		}
	}
	// stdin without --password-stdin is refused rather than guessed
	if code, _, _ := c.run(pw+"\n", "user", "create", "--username", "alice"); code == 0 {
		t.Error("created a user without --password-stdin")
	}
	for name, in := range map[string]string{"empty stdin": "", "blank line": "\n", "too short": "short\n"} {
		if code, out, errb := c.run(in, "user", "create", "--username", "alice", "--password-stdin"); code == 0 || strings.Contains(out+errb, pw) {
			t.Errorf("%s: exit %d", name, code)
		}
	}
	out, errb := c.mustRun(pw+"\r\n", "user", "create", "--username", "alice", "--password-stdin", "--admin")
	if strings.Contains(out+errb, pw) || !strings.Contains(out, "alice") {
		t.Errorf("create output: %q %q", out, errb)
	}
	// persisted with a verifiable Argon2id hash and collection roots
	db := c.db()
	ids := identity.New(db.SQL())
	u, err := ids.VerifyPassword(context.Background(), "alice", pw)
	if err != nil || !u.IsAdmin {
		t.Fatalf("verify: %+v %v", u, err)
	}
	var h string
	db.SQL().QueryRow(`SELECT password_hash FROM users WHERE username='alice'`).Scan(&h)
	if !strings.HasPrefix(h, "$argon2id$") || strings.Contains(h, pw) {
		t.Errorf("stored hash %q", h)
	}
	var roots int
	db.SQL().QueryRow(`SELECT COUNT(*) FROM folders WHERE owner_id=? AND parent_id IS NULL`, u.ID).Scan(&roots)
	if roots != 2 {
		t.Errorf("canonical roots = %d", roots)
	}
	// duplicates fail cleanly
	if code, _, _ := c.run(pw+"\n", "user", "create", "--username", "ALICE", "--password-stdin"); code != 1 {
		t.Errorf("duplicate user exit %d", code)
	}
}

func TestUserListAndSetActive(t *testing.T) {
	c := newCLI(t)
	c.mustRun(pw+"\n", "user", "create", "--username", "alice", "--password-stdin")
	c.mustRun(pw+"\n", "user", "create", "--username", "bob", "--password-stdin")
	out, _ := c.mustRun("", "user", "list")
	if !strings.Contains(out, "alice") || !strings.Contains(out, "bob") || strings.Contains(out, "argon2") {
		t.Errorf("list = %q", out)
	}
	c.mustRun("", "user", "set-active", "--username", "bob", "--active=false")
	ids := identity.New(c.db().SQL())
	if u, _ := ids.GetUser(context.Background(), "bob"); u.Active {
		t.Error("bob still active")
	}
	if code, _, _ := c.run("", "user", "set-active", "--username", "ghost", "--active=false"); code != 1 {
		t.Errorf("unknown user exit %d", code)
	}
}

func TestCredentialSecretIsPrintedOnceOnStdoutAndRevocable(t *testing.T) {
	c := newCLI(t)
	c.mustRun(pw+"\n", "user", "create", "--username", "alice", "--password-stdin")
	out, errb := c.mustRun("", "credential", "create", "--username", "alice", "--label", "laptop", "--mode", "edit")
	secret := strings.TrimSpace(out)
	if strings.Count(out, "\n") != 1 || !strings.HasPrefix(secret, "bms1.") || strings.ContainsAny(secret, " \t") {
		t.Fatalf("stdout must be exactly the secret line, got %q", out)
	}
	if strings.Contains(errb, secret) || !strings.Contains(strings.ToLower(errb), "not be shown again") {
		t.Errorf("stderr must warn and never repeat the secret: %q", errb)
	}
	db := c.db()
	ids := identity.New(db.SQL())
	p, err := ids.Authenticate(context.Background(), "alice", secret)
	if err != nil || p.Mode != identity.ModeEdit {
		t.Fatalf("authenticate: %+v %v", p, err)
	}
	// listing shows metadata, never secrets
	listOut, listErr := c.mustRun("", "credential", "list", "--username", "alice")
	if strings.Contains(listOut+listErr, strings.Split(secret, ".")[2]) || !strings.Contains(listOut, "laptop") || !strings.Contains(listOut, strconv.FormatInt(p.CredentialID, 10)) {
		t.Errorf("list output = %q", listOut)
	}
	// revoke
	c.mustRun("", "credential", "revoke", "--id", strconv.FormatInt(p.CredentialID, 10))
	if _, err := ids.Authenticate(context.Background(), "alice", secret); err == nil {
		t.Error("revoked credential still authenticates")
	}
	if code, _, _ := c.run("", "credential", "revoke", "--id", "999"); code != 1 {
		t.Errorf("revoke unknown exit %d", code)
	}
	// every create prints a fresh secret
	out2, _ := c.mustRun("", "credential", "create", "--username", "alice", "--mode", "view")
	if strings.TrimSpace(out2) == secret {
		t.Error("secret reused")
	}
	if code, _, _ := c.run("", "credential", "create", "--username", "alice", "--mode", "write"); code == 0 {
		t.Error("bad mode accepted")
	}
	if code, _, _ := c.run("", "credential", "create", "--username", "alice"); code == 0 {
		t.Error("missing --mode accepted: the permission must be explicit")
	}
	if code, _, _ := c.run("", "credential", "create", "--username", "ghost", "--mode", "view"); code != 1 {
		t.Errorf("unknown user exit %d", code)
	}
}

func TestShareGrantRevokeAndShareBoundCredential(t *testing.T) {
	c := newCLI(t)
	c.mustRun(pw+"\n", "user", "create", "--username", "alice", "--password-stdin")
	c.mustRun(pw+"\n", "user", "create", "--username", "bob", "--password-stdin")
	db := c.db()
	ctx := context.Background()
	ids := identity.New(db.SQL())
	svc := bookmarks.New(db.SQL())
	alice, _ := ids.GetUser(ctx, "alice")
	_, _ = ids.GetUser(ctx, "bob")
	scope := bookmarks.Scope{OwnerID: alice.ID, Writable: true}
	nodes, _ := svc.Children(ctx, scope, bookmarks.RootID, 1)
	folder, err := svc.CreateFolder(ctx, scope, nodes[0].ID, "Shared")
	if err != nil {
		t.Fatal(err)
	}
	fid := strconv.FormatInt(folder.ID, 10)

	// a bad grant fails and changes nothing
	if code, _, _ := c.run("", "share", "grant", "--owner", "alice", "--folder-id", fid, "--user", "ghost", "--mode", "view"); code != 1 {
		t.Errorf("unknown recipient exit %d", code)
	}
	if code, _, _ := c.run("", "share", "grant", "--owner", "alice", "--folder-id", fid, "--user", "bob", "--mode", "rw"); code == 0 {
		t.Error("bad mode accepted")
	}
	c.mustRun("", "share", "grant", "--owner", "alice", "--folder-id", fid, "--user", "bob", "--mode", "view")
	listOut, _ := c.mustRun("", "share", "list", "--owner", "alice")
	if !strings.Contains(listOut, "bob") || !strings.Contains(listOut, "view") {
		t.Errorf("share list = %q", listOut)
	}
	// share-bound credential needs the live grant
	if code, _, _ := c.run("", "credential", "create", "--username", "bob", "--mode", "view", "--share-owner", "alice", "--share-folder", "999999"); code != 1 {
		t.Errorf("credential for a missing grant exit %d", code)
	}
	out, _ := c.mustRun("", "credential", "create", "--username", "bob", "--mode", "view", "--share-owner", "alice", "--share-folder", fid)
	secret := strings.TrimSpace(out)
	p, err := ids.Authenticate(ctx, "bob", secret)
	if err != nil || p.Kind != identity.KindShare || p.ShareID == 0 {
		t.Fatalf("principal %+v err %v", p, err)
	}
	if _, err := svc.ResolveScope(ctx, p); err != nil {
		t.Fatalf("scope: %v", err)
	}
	// upgrade then revoke through the CLI: effective immediately
	c.mustRun("", "share", "grant", "--owner", "alice", "--folder-id", fid, "--user", "bob", "--mode", "edit")
	sc, _ := svc.ResolveScope(ctx, p)
	if sc.Writable {
		t.Error("view credential must stay read-only under an edit grant")
	}
	c.mustRun("", "share", "revoke", "--owner", "alice", "--folder-id", fid, "--user", "bob")
	if _, err := ids.Authenticate(ctx, "bob", secret); err == nil {
		t.Error("credential survived the revoked grant")
	}
	if code, _, _ := c.run("", "share", "revoke", "--owner", "alice", "--folder-id", fid, "--user", "bob"); code != 1 {
		t.Errorf("second revoke exit %d", code)
	}
}
