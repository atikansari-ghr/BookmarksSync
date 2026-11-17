package bookmarks

import (
	"context"
	"database/sql"
)

// ImportNode is one parsed item of a bulk import (Netscape HTML or any other source).
type ImportNode struct {
	Folder   bool
	Title    string
	URL      string
	Children []ImportNode
}

func countAndCheck(nodes []ImportNode, depth int, total *int) error {
	if depth > MaxImportDepth {
		return invalid("import nested deeper than %d levels", MaxImportDepth)
	}
	for _, n := range nodes {
		*total++
		if *total > MaxImportNodes {
			return invalid("import larger than %d items", MaxImportNodes)
		}
		if err := validTitle("title", n.Title); err != nil {
			return err
		}
		if n.Folder {
			if err := countAndCheck(n.Children, depth+1, total); err != nil {
				return err
			}
			continue
		}
		if err := validateURL(n.URL); err != nil {
			return err
		}
	}
	return nil
}

func importHeight(nodes []ImportNode) int {
	h := 0
	for _, n := range nodes {
		if n.Folder {
			if d := 1 + importHeight(n.Children); d > h {
				h = d
			}
		}
	}
	return h
}

// ImportTree appends nodes to a folder, in order, inside one transaction: either the
// whole tree is created or nothing is. It returns only what the call imported.
// Bookmarks whose exact URL is already visible in the scope reuse that record and gain
// a placement here; one already placed in this folder is skipped, never duplicated.
// URLs are stored only; nothing is fetched or executed.
func (s *Service) ImportTree(ctx context.Context, sc Scope, folderID int64, nodes []ImportNode) ([]Node, error) {
	total := 0
	if err := countAndCheck(nodes, 1, &total); err != nil {
		return nil, err
	}
	var out []Node
	err := s.write(ctx, sc, func(tx *sql.Tx) error {
		parent, err := s.visibleFolder(ctx, tx, sc, folderID)
		if err != nil {
			return err
		}
		if err := s.checkDepth(ctx, tx, realRoot(parent), importHeight(nodes)); err != nil {
			return err
		}
		out, err = s.importInto(ctx, tx, sc, parent, nodes)
		return err
	})
	return out, err
}

func (s *Service) importInto(ctx context.Context, tx *sql.Tx, sc Scope, parent frow, nodes []ImportNode) ([]Node, error) {
	out := []Node{}
	for _, n := range nodes {
		if n.Folder {
			if parent.virtual && isCanonicalTitle(n.Title) {
				return nil, invalid("%q is reserved for the canonical root folders", n.Title)
			}
			pos, err := s.nextPosition(ctx, tx, sc.OwnerID, realRoot(parent))
			if err != nil {
				return nil, err
			}
			now := s.ms()
			res, err := tx.ExecContext(ctx, `INSERT INTO folders(owner_id, parent_id, kind, title, position, created_at, updated_at) VALUES (?, ?, 'folder', ?, ?, ?, ?)`,
				sc.OwnerID, nullable(realRoot(parent)), n.Title, pos, now, now)
			if err != nil {
				return nil, err
			}
			id, _ := res.LastInsertId()
			child := frow{id: id, owner: sc.OwnerID, parent: realRoot(parent), kind: kindFolder, title: n.Title}
			kids, err := s.importInto(ctx, tx, sc, child, n.Children)
			if err != nil {
				return nil, err
			}
			out = append(out, Node{Type: typeFolder, ID: id, Title: n.Title, Loaded: true, Children: kids})
			continue
		}
		if parent.virtual {
			return nil, invalid("bookmarks cannot be placed in the root; use a folder")
		}
		id, found, err := s.findVisibleByURL(ctx, tx, sc, n.URL, []int64{parent.id})
		if err != nil {
			return nil, err
		}
		if !found {
			now := s.ms()
			res, err := tx.ExecContext(ctx, `INSERT INTO bookmarks(owner_id, url, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, sc.OwnerID, n.URL, n.Title, now, now)
			if err != nil {
				return nil, err
			}
			id, _ = res.LastInsertId()
		}
		created, err := s.addPlacement(ctx, tx, sc.OwnerID, id, parent.id)
		if err != nil {
			return nil, err
		}
		if !created {
			continue // already in this folder (or a duplicate within the import)
		}
		var title, url string
		if err := tx.QueryRowContext(ctx, `SELECT title, url FROM bookmarks WHERE id = ?`, id).Scan(&title, &url); err != nil {
			return nil, err
		}
		out = append(out, Node{Type: typeBookmark, ID: id, Title: title, URL: url})
	}
	return out, nil
}
