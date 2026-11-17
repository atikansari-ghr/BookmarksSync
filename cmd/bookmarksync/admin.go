package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// errUsage marks a mistake in how a command was invoked (exit status 2).
type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

func usageErr(format string, a ...any) error { return errUsage{fmt.Sprintf(format, a...)} }

type adminCmd struct {
	name    string
	summary string
	flags   string // flag help, shown by --help
	run     func(e *adminEnv, fs *flag.FlagSet) error
	setup   func(fs *flag.FlagSet, o *cmdOpts)
}

// cmdOpts holds flag values shared by the admin commands.
type cmdOpts struct {
	dataDir                            string
	username, label, mode, owner, user string
	shareOwner                         string
	folderID, shareFolder, id          int64
	admin, passwordStdin, active       bool
}

type adminEnv struct {
	ctx    context.Context
	db     *database.DB
	ids    *identity.Service
	svc    *bookmarks.Service
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	o      *cmdOpts
}

var adminGroups = map[string][]adminCmd{
	"user": {
		{name: "create", summary: "create a user (the password is read from standard input)",
			flags: "  --username NAME   login name (no ':' or control characters)\n  --password-stdin  read the password (at least 12 bytes) from the first line of standard input\n  --admin           give the user the admin role\n",
			setup: func(fs *flag.FlagSet, o *cmdOpts) {
				fs.StringVar(&o.username, "username", "", "")
				fs.BoolVar(&o.passwordStdin, "password-stdin", false, "")
				fs.BoolVar(&o.admin, "admin", false, "")
			}, run: userCreate},
		{name: "list", summary: "list users", setup: func(fs *flag.FlagSet, o *cmdOpts) {}, run: userList},
		{name: "set-active", summary: "enable or disable a user (disabling revokes all their credentials for good; reactivation needs new ones)",
			flags: "  --username NAME   the user\n  --active=true|false\n",
			setup: func(fs *flag.FlagSet, o *cmdOpts) {
				fs.StringVar(&o.username, "username", "", "")
				fs.BoolVar(&o.active, "active", true, "")
			}, run: userSetActive},
	},
	"credential": {
		{name: "create", summary: "create a per-device app credential; its secret is printed once",
			flags: "  --username NAME      the user the credential belongs to\n" +
				"  --mode view|edit     required: view = download-only (reads and lock only), edit = read and write\n" +
				"  --label TEXT         a name for the device\n" +
				"  --share-owner OWNER --share-folder FOLDER_ID\n" +
				"                       bind the credential to a folder OWNER has shared with --username\n" +
				"                       (default: the user's own private collection)\n\n" +
				"The secret is written once to standard output (nothing else is printed there) and\n" +
				"cannot be shown again; use it as the floccus password. Never reuse the website password.\n",
			setup: func(fs *flag.FlagSet, o *cmdOpts) {
				fs.StringVar(&o.username, "username", "", "")
				fs.StringVar(&o.mode, "mode", "", "")
				fs.StringVar(&o.label, "label", "", "")
				fs.StringVar(&o.shareOwner, "share-owner", "", "")
				fs.Int64Var(&o.shareFolder, "share-folder", 0, "")
			}, run: credentialCreate},
		{name: "list", summary: "list a user's credentials (never shows secrets)",
			flags: "  --username NAME\n",
			setup: func(fs *flag.FlagSet, o *cmdOpts) { fs.StringVar(&o.username, "username", "", "") }, run: credentialList},
		{name: "revoke", summary: "revoke a credential immediately",
			flags: "  --id ID   credential id from \"credential list\"\n",
			setup: func(fs *flag.FlagSet, o *cmdOpts) { fs.Int64Var(&o.id, "id", 0, "") }, run: credentialRevoke},
	},
	"share": {
		{name: "grant", summary: "share a folder with an existing user (or change the mode of an existing grant)",
			flags: "  --owner NAME      the folder's owner\n  --folder-id ID    the folder (not a Bookmarks Bar / Other Bookmarks root)\n  --user NAME       the recipient\n  --mode view|edit  required\n",
			setup: shareFlags(true), run: shareGrant},
		{name: "revoke", summary: "remove a grant; credentials bound to it stop working immediately",
			flags: "  --owner NAME\n  --folder-id ID\n  --user NAME\n",
			setup: shareFlags(false), run: shareRevoke},
		{name: "list", summary: "list the grants an owner has made",
			flags: "  --owner NAME\n",
			setup: func(fs *flag.FlagSet, o *cmdOpts) { fs.StringVar(&o.owner, "owner", "", "") }, run: shareList},
	},
}

func shareFlags(withMode bool) func(*flag.FlagSet, *cmdOpts) {
	return func(fs *flag.FlagSet, o *cmdOpts) {
		fs.StringVar(&o.owner, "owner", "", "")
		fs.Int64Var(&o.folderID, "folder-id", 0, "")
		fs.StringVar(&o.user, "user", "", "")
		if withMode {
			fs.StringVar(&o.mode, "mode", "", "")
		}
	}
}

func groupUsage(group string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "usage: bookmarksync %s <command> [flags]\n\ncommands:\n", group)
	for _, c := range adminGroups[group] {
		fmt.Fprintf(&sb, "  %-11s %s\n", c.name, c.summary)
	}
	fmt.Fprintf(&sb, "\nRun \"bookmarksync %s <command> --help\" for flags. Every command accepts --data-dir (default ./data, env BOOKMARKSYNC_DATA_DIR).\n", group)
	return sb.String()
}

func cmdUsage(group string, c adminCmd) string {
	return fmt.Sprintf("usage: bookmarksync %s %s [flags] [--data-dir dir]\n\n%s\n\nflags:\n%s", group, c.name, c.summary, c.flags)
}

func admin(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	group := args[0]
	if len(args) < 2 {
		fmt.Fprint(stderr, groupUsage(group))
		return 2
	}
	if args[1] == "--help" || args[1] == "-h" || args[1] == "help" {
		fmt.Fprint(stdout, groupUsage(group))
		return 0
	}
	var cmd *adminCmd
	for i := range adminGroups[group] {
		if adminGroups[group][i].name == args[1] {
			cmd = &adminGroups[group][i]
		}
	}
	if cmd == nil {
		fmt.Fprintf(stderr, "bookmarksync: unknown command %q\n%s", group+" "+args[1], groupUsage(group))
		return 2
	}
	o := &cmdOpts{dataDir: config.DefaultDataDir}
	if v := getenv(config.EnvDataDir); v != "" {
		o.dataDir = v
	}
	fs := flag.NewFlagSet(group+" "+cmd.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.dataDir, "data-dir", o.dataDir, "")
	cmd.setup(fs, o)
	if err := fs.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, cmdUsage(group, *cmd))
			return 0
		}
		fmt.Fprintf(stderr, "bookmarksync: %v\n\n%s", err, cmdUsage(group, *cmd))
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "bookmarksync: unexpected argument (passwords and secrets are never accepted as arguments)\n\n%s", cmdUsage(group, *cmd))
		return 2
	}
	db, err := database.Open(ctx, config.Config{DataDir: o.dataDir}.DatabasePath())
	if err != nil {
		fmt.Fprintf(stderr, "bookmarksync: %v\n", err)
		return 1
	}
	defer db.Close()
	e := &adminEnv{ctx: ctx, db: db, ids: identity.New(db.SQL()), svc: bookmarks.New(db.SQL()), stdin: stdin, stdout: stdout, stderr: stderr, o: o}
	if err := cmd.run(e, fs); err != nil {
		var u errUsage
		if errors.As(err, &u) {
			fmt.Fprintf(stderr, "bookmarksync: %v\n\n%s", err, cmdUsage(group, *cmd))
			return 2
		}
		fmt.Fprintf(stderr, "bookmarksync: %v\n", err)
		return 1
	}
	return 0
}

func (e *adminEnv) user(name string) (identity.User, error) {
	if name == "" {
		return identity.User{}, usageErr("a user name is required")
	}
	u, err := e.ids.GetUser(e.ctx, name)
	if errors.Is(err, identity.ErrNotFound) {
		return identity.User{}, fmt.Errorf("no such user %q", name)
	}
	return u, err
}

func parseMode(s string) (identity.Mode, error) {
	switch identity.Mode(s) {
	case identity.ModeView, identity.ModeEdit:
		return identity.Mode(s), nil
	}
	return "", usageErr("--mode must be \"view\" or \"edit\"")
}

func userCreate(e *adminEnv, _ *flag.FlagSet) error {
	if e.o.username == "" {
		return usageErr("--username is required")
	}
	if !e.o.passwordStdin {
		return usageErr("the password must be supplied on standard input with --password-stdin")
	}
	line, err := bufio.NewReader(io.LimitReader(e.stdin, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read password: %w", err)
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return errors.New("no password received on standard input")
	}
	u, err := e.ids.CreateUser(e.ctx, e.o.username, password, e.o.admin)
	if err != nil {
		return err
	}
	if err := e.svc.EnsureCollection(e.ctx, u.ID); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "created user %s (id %d, admin=%t)\n", u.Username, u.ID, u.IsAdmin)
	return nil
}

func userList(e *adminEnv, _ *flag.FlagSet) error {
	rows, err := e.db.SQL().QueryContext(e.ctx, `SELECT id, username, is_admin, active FROM users ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tUSERNAME\tADMIN\tACTIVE")
	for rows.Next() {
		var id int64
		var name string
		var admin, active int
		if err := rows.Scan(&id, &name, &admin, &active); err != nil {
			return err
		}
		fmt.Fprintf(tw, "%d\t%s\t%t\t%t\n", id, name, admin == 1, active == 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tw.Flush()
}

func userSetActive(e *adminEnv, _ *flag.FlagSet) error {
	if e.o.username == "" {
		return usageErr("--username is required")
	}
	if err := e.ids.SetActive(e.ctx, e.o.username, e.o.active); err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			return fmt.Errorf("no such user %q", e.o.username)
		}
		return err
	}
	fmt.Fprintf(e.stdout, "user %s active=%t\n", e.o.username, e.o.active)
	return nil
}

func credentialCreate(e *adminEnv, _ *flag.FlagSet) error {
	mode, err := parseMode(e.o.mode)
	if err != nil {
		return err
	}
	u, err := e.user(e.o.username)
	if err != nil {
		return err
	}
	nc := identity.NewCredential{UserID: u.ID, Label: e.o.label, Mode: mode}
	if e.o.shareOwner != "" || e.o.shareFolder != 0 {
		if e.o.shareOwner == "" || e.o.shareFolder == 0 {
			return usageErr("--share-owner and --share-folder must be given together")
		}
		owner, err := e.user(e.o.shareOwner)
		if err != nil {
			return err
		}
		sh, err := e.svc.FindShare(e.ctx, owner.ID, e.o.shareFolder, u.ID)
		if errors.Is(err, bookmarks.ErrNotFound) {
			return fmt.Errorf("%s has not shared folder %d with %s", owner.Username, e.o.shareFolder, u.Username)
		}
		if err != nil {
			return err
		}
		nc.Kind, nc.ShareID = identity.KindShare, sh.ID
	}
	c, secret, err := e.ids.CreateCredential(e.ctx, nc)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stderr, "Credential %d created for %s (%s, %s). The secret below will not be shown again; copy it now.\n", c.ID, u.Username, c.Kind, c.Mode)
	fmt.Fprintln(e.stdout, secret)
	return nil
}

func credentialList(e *adminEnv, _ *flag.FlagSet) error {
	u, err := e.user(e.o.username)
	if err != nil {
		return err
	}
	list, err := e.ids.ListCredentials(e.ctx, u.ID)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL\tKIND\tSHARE\tMODE\tCREATED\tLAST USED\tSTATE")
	for _, c := range list {
		last, state := "never", "active"
		if !c.LastUsedAt.IsZero() {
			last = c.LastUsedAt.Format("2006-01-02 15:04")
		}
		if c.Revoked {
			state = "revoked"
		}
		share := "-"
		if c.ShareID != 0 {
			share = fmt.Sprint(c.ShareID)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Label, c.Kind, share, c.Mode, c.CreatedAt.Format("2006-01-02 15:04"), last, state)
	}
	return tw.Flush()
}

func credentialRevoke(e *adminEnv, _ *flag.FlagSet) error {
	if e.o.id <= 0 {
		return usageErr("--id is required")
	}
	if err := e.ids.RevokeCredential(e.ctx, e.o.id); err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			return fmt.Errorf("no credential with id %d", e.o.id)
		}
		return err
	}
	fmt.Fprintf(e.stdout, "credential %d revoked\n", e.o.id)
	return nil
}

func (e *adminEnv) shareParties() (owner, recipient identity.User, err error) {
	if e.o.folderID <= 0 {
		return owner, recipient, usageErr("--folder-id is required")
	}
	if owner, err = e.user(e.o.owner); err != nil {
		return
	}
	recipient, err = e.user(e.o.user)
	return
}

func shareGrant(e *adminEnv, _ *flag.FlagSet) error {
	mode, err := parseMode(e.o.mode)
	if err != nil {
		return err
	}
	owner, recipient, err := e.shareParties()
	if err != nil {
		return err
	}
	sh, err := e.svc.GrantShare(e.ctx, owner.ID, e.o.folderID, recipient.ID, mode)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "share %d: %s's folder %d is shared with %s (%s)\n", sh.ID, owner.Username, sh.FolderID, recipient.Username, sh.Mode)
	return nil
}

func shareRevoke(e *adminEnv, _ *flag.FlagSet) error {
	owner, recipient, err := e.shareParties()
	if err != nil {
		return err
	}
	if err := e.svc.RevokeShare(e.ctx, owner.ID, e.o.folderID, recipient.ID); err != nil {
		if errors.Is(err, bookmarks.ErrNotFound) {
			return fmt.Errorf("no such share")
		}
		return err
	}
	fmt.Fprintf(e.stdout, "revoked: folder %d is no longer shared with %s\n", e.o.folderID, recipient.Username)
	return nil
}

func shareList(e *adminEnv, _ *flag.FlagSet) error {
	owner, err := e.user(e.o.owner)
	if err != nil {
		return err
	}
	list, err := e.svc.ListShares(e.ctx, owner.ID)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tFOLDER\tRECIPIENT\tMODE")
	for _, sh := range list {
		name := fmt.Sprint(sh.RecipientID)
		var n string
		if err := e.db.SQL().QueryRowContext(e.ctx, `SELECT username FROM users WHERE id = ?`, sh.RecipientID).Scan(&n); err == nil {
			name = n
		}
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\n", sh.ID, sh.FolderID, name, sh.Mode)
	}
	return tw.Flush()
}
