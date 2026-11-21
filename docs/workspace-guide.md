# Workspace and administration guide

This guide describes the signed-in BookmarksSync pages. Browser sign-in must be enabled with
`--public-url`; see [Authentication and account security](authentication.md). Accounts are
created by an administrator. Sign in with your username, then use the workspace navigation.

## Dashboard

Open **Dashboard** to see the total bookmark count, recent changes, latest browser sync, and
duplicate count. Use **Review duplicates** to open the duplicate review page. In **Visible
widgets**, show or hide cards; use each card's size menu to resize it. Move focus to a card and
press `Alt` plus `ArrowUp` or `ArrowDown` to change its order. The layout and appearance
preferences are saved to your account and follow you across browsers.

## Organize bookmarks

Open **My Bookmarks** to browse folders and bookmarks. Select a folder to see its contents; use
the folder controls to create, rename, move, reorder, or delete folders. Drag bookmarks between
folders, or select several bookmarks and move them together. Bookmark addresses, titles, tags,
and folder placements can be edited from the item actions. A bookmark can be favorited without
changing how it syncs through Floccus. Deleted items go to Trash, where they can be restored or
permanently removed.

Open **Duplicates** to review repeated addresses and remove unwanted copies. Use tags and search
to narrow the organizer list. The link checker checks only public HTTP(S) links and skips any
addresses or network ranges excluded in your account settings.

## Account appearance

Choose **Classic**, **Ocean Glass**, **Midnight Glass**, or **Warm Glass** from the dashboard's
appearance controls. Light/dark colors and reduced transparency are also saved per account.
Administrators can set theme defaults for individual accounts; a user's own choice takes
precedence over that default.

## Administrator pages

Administrators see **Administration** in the workspace navigation. The page provides:

- Account status, bookmark/folder counts, storage usage, browser connections, and per-account
  theme defaults.
- Host CPU, memory, disk, uptime, database readiness, and failed Floccus server-request counts.
  This view does not include bookmark titles, addresses, credentials, or secrets.
- A sanitized activity list showing fixed action names and numeric targets; it does not store
  free-form request or bookmark data.
- Login appearance settings: application/sign-in titles, tagline, supporting line, colors, and
  split, centered, or plain layout.
- Optional logo and background images. Upload PNG, JPEG, or WebP files up to 2 MiB. The server
  ignores the supplied filename and stores images under generated names in its private data
  directory. The images appear on the public login page.

Changes to administrator settings require a recent password confirmation. If prompted, enter
your current account password to continue. The account list and aggregate host-health view are
available only to active administrators.
