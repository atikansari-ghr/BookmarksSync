package floccus

import (
	"errors"
	"io"
	"net/http"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/interchange"
)

// maxImportBytes bounds the uploaded HTML (the server-wide body cap is also in force).
const maxImportBytes = 1 << 20

func (a *Adapter) importFolder(c *call) {
	id, ok := c.folderParam(0)
	if !ok {
		return
	}
	mr, err := c.r.MultipartReader()
	if err != nil {
		a.badRequest(c.w, "expected a multipart/form-data upload with a bm_import field")
		return
	}
	var nodes []bookmarks.ImportNode
	found := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			a.importReadError(c, err)
			return
		}
		if part.FormName() != "bm_import" {
			continue
		}
		nodes, err = parseNetscape(part)
		if err != nil {
			a.importReadError(c, err)
			return
		}
		found = true
		break
	}
	if !found {
		a.badRequest(c.w, "missing bm_import upload")
		return
	}
	out, err := a.svc.ImportTree(c.r.Context(), c.sc, id, nodes)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	imported, err := toNodes(out)
	if err != nil {
		a.internal(c.w, err)
		return
	}
	success(c.w, map[string]any{"data": imported})
}

func (a *Adapter) importReadError(c *call, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) || errors.Is(err, bookmarks.ErrInvalid) {
		a.fail(c.w, err)
		return
	}
	a.badRequest(c.w, "could not read the upload")
}

// parseNetscape reads the uploaded Netscape bookmark file with the shared, bounded
// parser (internal/interchange). Floccus imports keep their original behaviour: the
// 1 MiB cap and no TAGS attribute.
func parseNetscape(r io.Reader) ([]bookmarks.ImportNode, error) {
	return interchange.ParseNetscape(r, maxImportBytes, false)
}
