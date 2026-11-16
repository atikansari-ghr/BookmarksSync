package bookmarks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// The folder hash must equal what floccus computes locally (src/lib/Tree.ts):
//
//	bookmark = sha256(JSON.stringify({title, url}))
//	folder   = sha256(JSON.stringify({title, children: [childHash...]}))   in display order
//	root     = sha256(JSON.stringify({children: [...]}))                   (title undefined)
//
// JSON.stringify output is produced by jsString below, byte for byte: only the
// quote, backslash and C0 controls are escaped; "/", "<", ">", "&", DEL, U+2028 and
// U+2029 and all other non-ASCII stay literal (Go's encoding/json would escape
// U+2028/U+2029 and, by default, <>&). The Go string is UTF-8, which is exactly the
// byte sequence TextEncoder feeds to SHA-256.

func jsString(sb *strings.Builder, s string) {
	const hexd = "0123456789abcdef"
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			sb.WriteString(`\"`)
		case r == '\\':
			sb.WriteString(`\\`)
		case r == '\b':
			sb.WriteString(`\b`)
		case r == '\f':
			sb.WriteString(`\f`)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == '\t':
			sb.WriteString(`\t`)
		case r < 0x20:
			sb.WriteString(`\u00`)
			sb.WriteByte(hexd[r>>4])
			sb.WriteByte(hexd[r&0xf])
		case r == utf8.RuneError:
			// Invalid UTF-8 never reaches storage (validated on input); a literal
			// U+FFFD is encoded as itself.
			sb.WriteRune(r)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func childList(sb *strings.Builder, kids []string) {
	sb.WriteString(`"children":[`)
	for i, k := range kids {
		if i > 0 {
			sb.WriteByte(',')
		}
		jsString(sb, k)
	}
	sb.WriteString(`]`)
}

// HashBookmark is the sha256 hash of a bookmark's title and url.
func HashBookmark(title, url string) string {
	var sb strings.Builder
	sb.WriteString(`{"title":`)
	jsString(&sb, title)
	sb.WriteString(`,"url":`)
	jsString(&sb, url)
	sb.WriteByte('}')
	return sha256hex(sb.String())
}

// HashFolder is the hash of a titled folder given its children's hashes in order.
func HashFolder(title string, childHashes []string) string {
	var sb strings.Builder
	sb.WriteString(`{"title":`)
	jsString(&sb, title)
	sb.WriteByte(',')
	childList(&sb, childHashes)
	sb.WriteByte('}')
	return sha256hex(sb.String())
}

// HashRoot is the hash of a root (-1), which has no title.
func HashRoot(childHashes []string) string {
	var sb strings.Builder
	sb.WriteByte('{')
	childList(&sb, childHashes)
	sb.WriteByte('}')
	return sha256hex(sb.String())
}

func (t *tree) hashFolder(id int64, root bool, title string) string {
	var kids []string
	for _, k := range t.kids[id] {
		if k.folder {
			kids = append(kids, t.hashFolder(k.id, false, k.title))
		} else {
			kids = append(kids, HashBookmark(k.title, k.url))
		}
	}
	if root {
		return HashRoot(kids)
	}
	return HashFolder(title, kids)
}

// FolderHash returns the sha256 hash of a folder subtree as floccus computes it.
// The scope root (virtual root or share root) hashes without a title. The hash is
// computed from current rows on every call, so it can never be stale.
func (s *Service) FolderHash(ctx context.Context, sc Scope, id int64) (string, error) {
	var out string
	err := s.read(ctx, func(q queryer) error {
		f, err := s.visibleFolder(ctx, q, sc, id)
		if err != nil {
			return err
		}
		t, err := s.loadTree(ctx, q, sc.OwnerID, realRoot(f))
		if err != nil {
			return err
		}
		out = t.hashFolder(realRoot(f), f.isRoot(), f.title)
		return nil
	})
	return out, err
}
