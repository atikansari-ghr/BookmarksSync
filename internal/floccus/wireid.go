package floccus

import (
	"errors"
	"regexp"
	"strconv"
)

// Folder ids on the Floccus wire.
//
// Stock floccus 5.13.0 compares ids from the browser's own bookmark tree with ids from the server,
// so a server folder whose number equals a local folder's number can be confused with it (traced in
// the browser harness report). The server therefore never puts a positive concrete folder id on this
// wire: internal (SQLite) folder n is wire -(n+1), and -1 stays the virtual root. Bookmark ids stay
// positive and are never translated. Only this package knows the mapping; the domain service and the
// database keep positive folder ids.
//
//	internal 1 <-> wire -2 (Bookmarks Bar)     internal 2 <-> wire -3 (Other Bookmarks)
//	root sentinel -1 (virtual root)            bookmark n <-> wire n

const (
	// wireRoot is the virtual root, as before. It is not a concrete folder.
	wireRoot int64 = -1
	// maxFolderID keeps every wire folder id within 18 digits, matching the digit limit used for ids elsewhere.
	maxFolderID int64 = 999999999999999998
	// maxBookmarkID is the largest 18-digit bookmark id.
	maxBookmarkID int64 = 999999999999999999
)

var (
	errFolderIDSyntax = errors.New("invalid folder id")
	errFolderIDRange  = errors.New("folder id out of range")
	errFolderIDRoot   = errors.New("the root is not a concrete folder")
	errBookmarkID     = errors.New("invalid bookmark id")
)

type refKind uint8

const (
	refInvalid refKind = iota // the zero value is never a usable reference
	refRoot
	refFolder
)

// folderRef is a folder reference: either the virtual root or one concrete folder.
type folderRef struct {
	kind refKind
	id   int64 // internal (positive) folder id when kind == refFolder
}

func rootRef() folderRef { return folderRef{kind: refRoot} }

// IsRoot reports whether the reference is the virtual root.
func (r folderRef) IsRoot() bool { return r.kind == refRoot }

// Wire is the id as it appears on the wire (-1 for the root, -(n+1) for internal folder n).
func (r folderRef) Wire() int64 {
	switch r.kind {
	case refRoot:
		return wireRoot
	case refFolder:
		return -(r.id + 1)
	}
	return 0
}

// Concrete returns the internal folder id. The root is not a concrete folder.
func (r folderRef) Concrete() (int64, error) {
	switch r.kind {
	case refRoot:
		return 0, errFolderIDRoot
	case refFolder:
		return r.id, nil
	}
	return 0, errFolderIDSyntax
}

// folderRefFromInternal turns a positive SQLite folder id into a reference.
func folderRefFromInternal(id int64) (folderRef, error) {
	if id <= 0 {
		return folderRef{}, errFolderIDSyntax
	}
	if id > maxFolderID {
		return folderRef{}, errFolderIDRange
	}
	return folderRef{kind: refFolder, id: id}, nil
}

// folderRefFromWire accepts -1 (root) or a concrete wire id <= -2. Zero and positive ids are never valid.
func folderRefFromWire(wire int64) (folderRef, error) {
	switch {
	case wire == wireRoot:
		return rootRef(), nil
	case wire >= 0:
		return folderRef{}, errFolderIDSyntax
	case wire < -(maxFolderID + 1): // includes math.MinInt64, whose negation would overflow
		return folderRef{}, errFolderIDRange
	}
	return folderRef{kind: refFolder, id: -wire - 1}, nil
}

// folderIDFromWire is the common case: a concrete folder is required, the root is rejected.
func folderIDFromWire(wire int64) (int64, error) {
	ref, err := folderRefFromWire(wire)
	if err != nil {
		return 0, err
	}
	return ref.Concrete()
}

var wireFolderRe = regexp.MustCompile(`^-[1-9][0-9]*$`)

// parseWireFolder parses the textual wire form (path segment, query value or numeric string). Only
// canonical negative decimals are accepted: no sign other than "-", no leading zeros, no spaces,
// no floats or exponents. Over-long numbers are a range error, everything else a syntax error.
func parseWireFolder(s string) (folderRef, error) {
	if !wireFolderRe.MatchString(s) {
		return folderRef{}, errFolderIDSyntax
	}
	if len(s)-1 > 18 {
		return folderRef{}, errFolderIDRange
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return folderRef{}, errFolderIDRange
	}
	return folderRefFromWire(n)
}

// bookmarkID is a bookmark id. Bookmark ids are positive and identical on the wire and inside.
type bookmarkID int64

// Wire is the id as it appears on the wire (unchanged).
func (b bookmarkID) Wire() int64 { return int64(b) }

func bookmarkIDFromInternal(id int64) (bookmarkID, error) {
	if id <= 0 || id > maxBookmarkID {
		return 0, errBookmarkID
	}
	return bookmarkID(id), nil
}
