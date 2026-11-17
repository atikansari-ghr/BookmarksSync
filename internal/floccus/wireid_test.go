package floccus

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

// The Floccus wire never carries a positive concrete folder id: internal folder n is wire -(n+1),
// the virtual root keeps -1, bookmark ids are untouched.

func TestInternalToWireFolderIDs(t *testing.T) {
	cases := []struct{ internal, wire int64 }{
		{1, -2}, {2, -3}, {3, -4}, {41, -42}, {999999999999999998, -999999999999999999},
	}
	for _, c := range cases {
		ref, err := folderRefFromInternal(c.internal)
		if err != nil {
			t.Fatalf("internal %d: %v", c.internal, err)
		}
		if ref.IsRoot() {
			t.Errorf("internal %d reported as root", c.internal)
		}
		if got := ref.Wire(); got != c.wire {
			t.Errorf("internal %d -> wire %d, want %d", c.internal, got, c.wire)
		}
		if id, err := ref.Concrete(); err != nil || id != c.internal {
			t.Errorf("Concrete() = %d, %v; want %d", id, err, c.internal)
		}
	}
}

func TestWireToInternalFolderIDs(t *testing.T) {
	for _, c := range []struct{ wire, internal int64 }{{-2, 1}, {-3, 2}, {-4, 3}, {-42, 41}, {-999999999999999999, 999999999999999998}} {
		ref, err := folderRefFromWire(c.wire)
		if err != nil || ref.IsRoot() {
			t.Fatalf("wire %d: %v root=%v", c.wire, err, ref.IsRoot())
		}
		if id, err := ref.Concrete(); err != nil || id != c.internal {
			t.Errorf("wire %d -> internal %d, %v; want %d", c.wire, id, err, c.internal)
		}
		if id, err := folderIDFromWire(c.wire); err != nil || id != c.internal {
			t.Errorf("folderIDFromWire(%d) = %d, %v; want %d", c.wire, id, err, c.internal)
		}
		// round trip
		if back := ref.Wire(); back != c.wire {
			t.Errorf("round trip %d -> %d", c.wire, back)
		}
	}
}

func TestRootSentinelIsDistinctFromConcreteFolders(t *testing.T) {
	ref, err := folderRefFromWire(-1)
	if err != nil || !ref.IsRoot() || ref.Wire() != -1 {
		t.Fatalf("-1 must be the root reference: %+v %v", ref, err)
	}
	if _, err := ref.Concrete(); !errors.Is(err, errFolderIDRoot) {
		t.Errorf("root used where a concrete folder is required: err = %v, want errFolderIDRoot", err)
	}
	if _, err := folderIDFromWire(-1); !errors.Is(err, errFolderIDRoot) {
		t.Errorf("folderIDFromWire(-1) err = %v, want errFolderIDRoot", err)
	}
	if r := rootRef(); !r.IsRoot() || r.Wire() != -1 {
		t.Errorf("rootRef() = %+v", r)
	}
	// the root can never be produced from an internal id
	if _, err := folderRefFromInternal(-1); err == nil {
		t.Error("internal -1 accepted as a folder id")
	}
}

func TestInvalidConcreteWireFolderIDsAreRejected(t *testing.T) {
	for _, wire := range []int64{0, 1, 2, 12345, math.MaxInt64} { // zero and positive ids never exist on the wire
		if _, err := folderRefFromWire(wire); !errors.Is(err, errFolderIDSyntax) {
			t.Errorf("wire %d: err = %v, want errFolderIDSyntax", wire, err)
		}
		if _, err := folderIDFromWire(wire); err == nil {
			t.Errorf("folderIDFromWire(%d) accepted", wire)
		}
	}
	if _, err := folderRefFromInternal(0); !errors.Is(err, errFolderIDSyntax) {
		t.Errorf("internal 0: err = %v, want errFolderIDSyntax", err)
	}
	if _, err := folderRefFromInternal(-5); err == nil {
		t.Error("negative internal id accepted")
	}
}

func TestFolderIDOverflowIsRejectedBothWays(t *testing.T) {
	// 19-digit wire ids and ids beyond the supported range
	for _, wire := range []int64{-1000000000000000000, math.MinInt64, math.MinInt64 + 1} {
		if _, err := folderRefFromWire(wire); !errors.Is(err, errFolderIDRange) {
			t.Errorf("wire %d: err = %v, want errFolderIDRange", wire, err)
		}
	}
	for _, internal := range []int64{999999999999999999, math.MaxInt64 - 1, math.MaxInt64} {
		if _, err := folderRefFromInternal(internal); !errors.Is(err, errFolderIDRange) {
			t.Errorf("internal %d: err = %v, want errFolderIDRange", internal, err)
		}
	}
}

func TestParseWireFolderText(t *testing.T) {
	good := map[string]struct {
		root     bool
		internal int64
	}{"-1": {true, 0}, "-2": {false, 1}, "-3": {false, 2}, "-42": {false, 41}, "-999999999999999999": {false, 999999999999999998}}
	for s, want := range good {
		ref, err := parseWireFolder(s)
		if err != nil {
			t.Errorf("%q: %v", s, err)
			continue
		}
		if ref.IsRoot() != want.root {
			t.Errorf("%q root = %v", s, ref.IsRoot())
		}
		if !want.root {
			if id, _ := ref.Concrete(); id != want.internal {
				t.Errorf("%q -> %d, want %d", s, id, want.internal)
			}
		}
	}
	for _, s := range []string{"", "0", "-0", "1", "2", "+2", "+-2", "-+2", "--2", "-02", "-002", "- 2", " -2", "-2 ", "-2.0", "-1e3", "-0x2", "abc", "-", "٣", "-٣", "null", "1;2", "-2;3", "%2d2"} {
		if _, err := parseWireFolder(s); !errors.Is(err, errFolderIDSyntax) {
			t.Errorf("%q: err = %v, want errFolderIDSyntax", s, err)
		}
	}
	for _, s := range []string{"-1000000000000000000", "-9223372036854775808", "-9223372036854775809", "-99999999999999999999"} {
		if _, err := parseWireFolder(s); !errors.Is(err, errFolderIDRange) {
			t.Errorf("%q: err = %v, want errFolderIDRange", s, err)
		}
	}
}

func TestBookmarkIDsAreNeverTranslated(t *testing.T) {
	for _, n := range []int64{1, 2, 3, 42, 999999999999999999} {
		b, err := bookmarkIDFromInternal(n)
		if err != nil {
			t.Fatalf("bookmark %d: %v", n, err)
		}
		if b.Wire() != n {
			t.Errorf("bookmark %d translated to %d: bookmark ids must stay as they are", n, b.Wire())
		}
	}
	for _, n := range []int64{0, -1, -2, math.MinInt64} {
		if _, err := bookmarkIDFromInternal(n); err == nil {
			t.Errorf("bookmark id %d accepted", n)
		}
	}
	// the same number means different things for a folder and a bookmark
	f, _ := folderRefFromInternal(7)
	b, _ := bookmarkIDFromInternal(7)
	if f.Wire() == b.Wire() {
		t.Errorf("folder 7 and bookmark 7 share the wire id %d", f.Wire())
	}
	// no concrete folder ever lands on the positive side of the wire
	for n := int64(1); n <= 1000; n++ {
		ref, _ := folderRefFromInternal(n)
		if ref.Wire() >= -1 {
			t.Fatalf("internal folder %d -> wire %d collides with the root or the positive space", n, ref.Wire())
		}
	}
}

func TestWireFolderRoundTripThroughText(t *testing.T) {
	for n := int64(1); n <= 300; n++ {
		ref, err := folderRefFromInternal(n)
		if err != nil {
			t.Fatal(err)
		}
		back, err := parseWireFolder(strconv.FormatInt(ref.Wire(), 10))
		if err != nil {
			t.Fatal(err)
		}
		if id, _ := back.Concrete(); id != n {
			t.Fatalf("%d -> %d -> %d", n, ref.Wire(), id)
		}
	}
}
