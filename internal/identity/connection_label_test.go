package identity_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// approveFlowWithLabel begins a Login Flow, approves it for a fresh session of u
// with label, and returns the poll handle and approval error. A rejected approval
// must leave the flow pending.
func approveFlowWithLabel(t *testing.T, svc *identity.Service, u identity.User, label string) (poll string, err error) {
	t.Helper()
	ctx := context.Background()
	browser, poll, err := svc.BeginLoginFlow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := svc.CreateSession(ctx, u.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApproveLoginFlow(ctx, browser, sess.ID, label); err != nil {
		if pendErr := svc.LoginFlowPending(ctx, browser); pendErr != nil {
			t.Fatalf("rejected approval changed flow state: %v", pendErr)
		}
		return poll, err
	}
	return poll, nil
}

func credentialLabel(t *testing.T, svc *identity.Service, userID, credID int64) string {
	t.Helper()
	cs, err := svc.ListCredentials(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.ID == credID {
			return c.Label
		}
	}
	t.Fatalf("credential %d not listed", credID)
	return ""
}

func TestLoginFlowApprovedLabelPersistsIntoCredential(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	u, err := svc.CreateUser(ctx, "alice", goodPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("é", 200)
	for _, tc := range [][2]string{
		{"BookmarksSync-Home-Firefox", "BookmarksSync-Home-Firefox"},
		{"  \tBookmarksSync-Home-Firefox \n", "BookmarksSync-Home-Firefox"},
		{"Laptop — Ünïcode 書签", "Laptop — Ünïcode 書签"},
		{long, long},
		{identity.DefaultLoginFlowLabel, "Login Flow v2 connection"},
		{"Inner  spaces are kept", "Inner  spaces are kept"},
		{strings.Repeat(" ", 3) + long + "\t\t\t", long},
	} {
		input, want := tc[0], tc[1]
		poll, err := approveFlowWithLabel(t, svc, u, input)
		if err != nil {
			t.Fatalf("approve %q: %v", input, err)
		}
		username, secret, err := svc.PollLoginFlow(ctx, poll)
		if err != nil || username != "alice" {
			t.Fatalf("poll %q: %q %v", input, username, err)
		}
		p, err := svc.Authenticate(ctx, "alice", secret)
		if err != nil || p.Kind != identity.KindPrivate || p.Mode != identity.ModeEdit || p.ShareID != 0 {
			t.Fatalf("minted credential for %q = %+v %v", input, p, err)
		}
		if got := credentialLabel(t, svc, u.ID, p.CredentialID); got != want {
			t.Fatalf("label for %q = %q, want %q", input, got, want)
		}
	}
	if identity.DefaultLoginFlowLabel != "Login Flow v2 connection" {
		t.Fatalf("legacy default changed: %q", identity.DefaultLoginFlowLabel)
	}
}

func TestLoginFlowRejectsInvalidLabelsWithoutConsuming(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	u, err := svc.CreateUser(ctx, "alice", goodPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"", "   ", "\t\n", strings.Repeat("x", 201), strings.Repeat("é", 201),
		"a\x00b", "a\nb", "a\tb", "a\u0007b", "a\u0085b", "a\x7fb", "\xff\xfe",
	} {
		poll, err := approveFlowWithLabel(t, svc, u, bad)
		if !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("label %q: err=%v, want ErrInvalid", bad, err)
		}
		if _, _, err := svc.PollLoginFlow(ctx, poll); !errors.Is(err, identity.ErrNotFound) {
			t.Fatalf("label %q: rejected approval still minted: %v", bad, err)
		}
	}
	if cs, _ := svc.ListCredentials(ctx, u.ID); len(cs) != 0 {
		t.Fatalf("invalid labels minted %d credentials", len(cs))
	}
}

func TestRenameCredentialIsOwnerOnlyAndMetadataOnly(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	alice, err := svc.CreateUser(ctx, "alice", goodPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := svc.CreateUser(ctx, "bob", goodPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	cred, secret, err := svc.CreateCredential(ctx, identity.NewCredential{UserID: alice.ID, Label: "Login Flow v2 connection", Mode: identity.ModeView})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, "alice", secret); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		var out string
		err := db.SQL().QueryRowContext(ctx, `SELECT user_id||'|'||secret_hash||'|'||kind||'|'||coalesce(share_id,'-')||'|'||mode||'|'||created_at||'|'||coalesce(last_used_at,'-')||'|'||coalesce(revoked_at,'-') FROM credentials WHERE id=?`, cred.ID).Scan(&out)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot()
	var credCount int
	countCreds := func() int {
		if err := db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM credentials`).Scan(&credCount); err != nil {
			t.Fatal(err)
		}
		return credCount
	}
	total := countCreds()

	if _, err := svc.RenameCredential(ctx, bob.ID, cred.ID, "pwned"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("bob renamed alice's credential: %v", err)
	}
	if _, err := svc.RenameCredential(ctx, alice.ID, cred.ID+1000, "x"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("nonexistent credential: %v", err)
	}
	for _, bad := range []string{"", "  ", strings.Repeat("x", 201), "a\nb", "a\x00b", "\xff"} {
		if _, err := svc.RenameCredential(ctx, alice.ID, cred.ID, bad); !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("label %q: %v, want ErrInvalid", bad, err)
		}
	}
	if got := credentialLabel(t, svc, alice.ID, cred.ID); got != "Login Flow v2 connection" {
		t.Fatalf("failed renames changed label to %q", got)
	}

	info, err := svc.RenameCredential(ctx, alice.ID, cred.ID, "  BookmarksSync-Home-Firefox  ")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != cred.ID || info.UserID != alice.ID || info.Label != "BookmarksSync-Home-Firefox" || info.Kind != identity.KindPrivate || info.Mode != identity.ModeView || info.ShareID != 0 || info.Revoked || info.CreatedAt.IsZero() {
		t.Fatalf("rename result = %+v", info)
	}
	if got := credentialLabel(t, svc, alice.ID, cred.ID); got != "BookmarksSync-Home-Firefox" {
		t.Fatalf("label after rename = %q", got)
	}
	if after := snapshot(); after != before {
		t.Fatalf("rename changed non-label columns:\n before %s\n after  %s", before, after)
	}
	if countCreds() != total {
		t.Fatal("rename created or removed credentials")
	}
	p, err := svc.Authenticate(ctx, "alice", secret)
	if err != nil || p.CredentialID != cred.ID || p.Mode != identity.ModeView {
		t.Fatalf("secret after rename = %+v %v", p, err)
	}

	if err := svc.RevokeUserCredential(ctx, alice.ID, cred.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RenameCredential(ctx, alice.ID, cred.ID, "after revoke"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("revoked credential renamed: %v", err)
	}
	if got := credentialLabel(t, svc, alice.ID, cred.ID); got != "BookmarksSync-Home-Firefox" {
		t.Fatalf("revoked credential label changed to %q", got)
	}
}
