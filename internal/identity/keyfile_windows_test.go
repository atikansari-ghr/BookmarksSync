//go:build windows

package identity_test

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func currentSID(t *testing.T) *windows.SID {
	t.Helper()
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := tu.User.Sid.Copy()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func setSDDL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

// writePrivateFile writes content with the owner-only ACL the service requires, so a
// test exercises content validation rather than permission checks.
func writePrivateFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	setSDDL(t, path, "D:P(A;;FA;;;"+currentSID(t).String()+")")
}

type aceView struct {
	typ  uint8
	sid  string
	mask windows.ACCESS_MASK
}

func fileACEs(t *testing.T, path string) (bool, []aceView) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("no DACL: %v", err)
	}
	var out []aceView
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		out = append(out, aceView{typ: ace.Header.AceType, sid: sid.String(), mask: ace.Mask})
	}
	return control&windows.SE_DACL_PROTECTED != 0, out
}

func TestInstanceKeyFileHasOwnerOnlyProtectedACLOnWindows(t *testing.T) {
	dir := t.TempDir()
	if _, err := identity.LoadOrCreateInstanceKey(dir); err != nil {
		t.Fatal(err)
	}
	protected, aces := fileACEs(t, filepath.Join(dir, identity.InstanceKeyFile))
	if !protected {
		t.Error("DACL inherits from the data directory; want a protected DACL")
	}
	me := currentSID(t).String()
	if len(aces) != 1 || aces[0].typ != windows.ACCESS_ALLOWED_ACE_TYPE || aces[0].sid != me {
		t.Fatalf("ACEs = %+v, want exactly one allow ACE for %s", aces, me)
	}
}

func TestLegacyDefaultOwnerInstanceKeyRemainsLoadable(t *testing.T) {
	dir := t.TempDir()
	want, err := identity.LoadOrCreateInstanceKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, identity.InstanceKeyFile)
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	info := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, adminSID, nil, nil, nil); err != nil {
		t.Skipf("cannot simulate an elevated legacy owner: %v", err)
	}
	got, err := identity.LoadOrCreateInstanceKey(dir)
	if err != nil {
		t.Fatalf("legacy protected key was rejected: %v", err)
	}
	if string(got) != string(want) {
		t.Fatal("migration changed instance key contents")
	}
	protected, aces := fileACEs(t, path)
	if !protected || len(aces) != 1 || aces[0].sid != currentSID(t).String() {
		t.Fatalf("migrated ACL is not current-user-only: protected=%v aces=%+v", protected, aces)
	}
}

func TestInstanceKeyWithBroadACLFailsClosedOnWindows(t *testing.T) {
	for name, extra := range map[string]string{
		"everyone read": "(A;;FR;;;WD)",
		"users read":    "(A;;FR;;;BU)",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			key, err := identity.LoadOrCreateInstanceKey(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, identity.InstanceKeyFile)
			setSDDL(t, path, "D:P(A;;FA;;;"+currentSID(t).String()+")"+extra)
			if _, err := identity.LoadOrCreateInstanceKey(dir); err == nil {
				t.Fatal("key readable by other principals was accepted")
			}
			if got, _ := os.ReadFile(path); string(got) != string(key) {
				t.Fatal("key was replaced")
			}
		})
	}
	t.Run("inherited ACL", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, identity.InstanceKeyFile)
		// A plain file inherits the directory's ACL (not protected).
		if err := os.WriteFile(path, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := identity.LoadOrCreateInstanceKey(dir); err == nil {
			t.Fatal("key with an inherited ACL was accepted")
		}
	})
}
