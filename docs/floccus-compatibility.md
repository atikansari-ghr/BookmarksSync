# floccus compatibility status

BookmarksSync speaks the subset of the Nextcloud Bookmarks REST API (v2) that the
stock floccus extension (**v5.13.0**, `nextcloud-bookmarks` adapter) actually uses. This
page separates what has been **read in source**, what is **covered by protocol tests**,
and what has been **verified in a real browser**. Those are different levels of
evidence; only the last one proves compatibility.

## Evidence levels

| Level | What it means | Status |
|---|---|---|
| Source inspection | Behaviour read in the floccus v5.13.0 and Nextcloud Bookmarks v17.0.2 source (route, verb, envelope and hash contract). | Done (`protocol-audit-report.md`, controller-held). |
| Protocol tests | `go test` drives the real HTTP handler over a real temporary SQLite database with requests shaped like floccus's. These prove the server matches the contract as read from source. | Done, see below. |
| **Real browser results** | Unmodified floccus in Chrome, Edge, Firefox and Brave syncing against this server. | **NOT RUN. Pending** (see "Pending browser checks"). No claim of browser compatibility is made. |

## Implemented routes

All routes exist under both `/index.php/apps/bookmarks/public/rest/v2/...` and the
indexless alias `/apps/bookmarks/public/rest/v2/...`. Nothing is ever redirected (floccus
treats a redirect as an error). Every response is JSON; success is
`{"status":"success", ...}`, errors are `{"status":"error","data":["message"]}`. No
response contains a `ticket` field.

| Verb + path | Notes |
|---|---|
| `GET /ocs/v2.php/cloud/capabilities` | OCS envelope. Advertises `javascript-bookmarks: true` and `hash-functions: ["sha256"]` only. |
| `GET bookmark?page&limit` | Visible bookmarks ordered by id. `limit` is 1..1000 (larger is clamped; 0, negative or non-numeric is 400). `page=-1` returns all matches (max 50000, else 400). |
| `GET bookmark?url=` | Exact, byte-for-byte URL lookup among bookmarks **visible to the credential**. |
| `GET bookmark?page=-1&search[]=javascript:` | Search terms are matched (case-insensitively, `%`/`_` literal) in url, title and tags. |
| `GET/POST/PUT/DELETE bookmark[/{id}]`, `POST bookmark/click` | `folders[]` lists only placements visible in the credential's scope. Omitted `tags` leave server tags untouched. `click` validates and acknowledges; no statistics exist. |
| `DELETE folder/{id}/bookmarks/{bid}` | Removes one placement; the bookmark survives while another placement exists. |
| `GET folder?root=&layers=` | Folder listing (for `serverRoot` path walking). |
| `POST folder`, `PUT/DELETE folder/{id}` | Create, rename/move, delete subtree (to Trash). |
| `GET folder/{id}/children?layers=-1\|N` | A folder carries `children` only when loaded (empty loaded folders return `[]`). Children keep the stored mixed folder/bookmark order. |
| `GET folder/{id}/hash?hashFn=sha256` | Hash matches floccus's JS (`JSON.stringify` + SHA-256). Any other `hashFn`, or tag fields, is 400. The root hashes without a title. |
| `PATCH folder/{id}/childorder` | `{"data":[{"id":"123","type":"bookmark\|folder"}]}`. Duplicates, unknown or foreign children reject the whole request. Children not listed are kept after the listed ones in their previous order. |
| `POST folder/{id}/import` | Multipart field `bm_import`, Netscape HTML, parsed with a real HTML5 parser. Appends to the folder and returns only the imported tree. At most 2000 items, 32 levels, 1 MiB. URLs are stored only; nothing is fetched or executed. All-or-nothing. |
| `POST/DELETE lock` | Scope lock, see below. |

Folder and bookmark ids use distinct wire conventions. `-1` is the virtual root; every
concrete Floccus folder id is negative (`wire = -(internal id + 1)`). For example, internal
folder `1` is sent as `-2`. Bookmark ids remain positive and unchanged. Folder ids in paths
and folder-valued JSON fields must be canonical negative integers; malformed values such as
`-0`, `-02`, `1.5`, `1e3` or `abc` return 400. Bookmark IDs keep their existing positive
form. SQLite and CLI folder IDs remain positive, so do not copy a CLI `--folder-id` value
into a Floccus request.

Folder items at a scope root report `parent_folder: 0` because they have no concrete parent;
zero is not a usable folder ID in a request.

`POST index.php/login/v2` (Login Flow v2) is **not implemented**; see "Setting up a floccus profile".

## Authentication and credentials

* Every route (including capabilities) requires HTTP Basic `username:app-secret`. Query
  parameters, cookies and `Bearer` headers are never accepted as credentials.
* The **website password never works for floccus**. App secrets are separate, per
  device, random (256 bits), stored only as a SHA-256 hash, compared in constant time,
  and shown **once** by the CLI. Revoking a credential, deactivating the user, revoking
  or trashing the share a credential is bound to all take effect on the next request, and
  every write re-checks the live credential, user and grant inside its own transaction (a
  revoke or downgrade that lands while a request body is still arriving wins).
  **Deactivating a user revokes all of their credentials in the same transaction** (and drops
  their locks); reactivating the user does not revive them, new credentials must be created.
* Failures are a generic `401 {"status":"error","data":["unauthorized"]}`, identical for
  unknown user, wrong secret, revoked and inactive. No `WWW-Authenticate` header is sent
  (floccus supplies credentials itself and a challenge could trigger a browser prompt).
* The request log (`floccus request` lines on stderr) records method, path (never the
  query), credential id and status; never secrets or headers.

### Scopes

A credential is bound to either the user's **private collection** or **one shared
folder**, and is `view` or `edit`.

* Private scope: root `-1` contains the canonical, undeletable `Bookmarks Bar` and
  `Other Bookmarks` folders (and any extra top-level folders, e.g. Firefox's extra roots).
  Incoming shares are **never** mounted here.
* Share scope: `-1` is the shared folder; its concrete negative Floccus folder id is accepted
  as an alias. The CLI continues to use the positive internal folder id.
  Only that subtree is visible. A bookmark placed directly in it reports `folders: [-1]`.
* `view` credentials (or view grants) get 403 `read-only share or credential` on every
  content write. They may still take and release the lock, which is all a floccus
  download-only sync writes. The effective right is the lower of grant and credential.
* Out-of-scope ids behave as if they do not exist (404).

### Provisioning (CLI)

Passwords are read only from standard input; secrets appear once on standard output.

```sh
printf '%s\n' "a long password" | bookmarksync user create --username alice --password-stdin --admin
bookmarksync credential create --username alice --label "work laptop" --mode edit     # prints the secret once
bookmarksync credential create --username alice --label "download-only" --mode view
bookmarksync share grant --owner alice --folder-id 12 --user bob --mode view
bookmarksync credential create --username bob --mode view --share-owner alice --share-folder 12
bookmarksync credential list --username alice          # metadata only, never secrets
bookmarksync credential revoke --id 3
bookmarksync share revoke --owner alice --folder-id 12 --user bob
bookmarksync user set-active --username bob --active=false
```

Every command has `--help`. The CLI writes to the same database as `serve`; run it on
the server host with the same `--data-dir`.

## Locking

One lock per credential, keyed by the **owner collection** it touches. A bookmark record can
be placed in several shared folders and in the owner's private tree, so a write through one
scope can change what a sibling scope sees; every scope under the same owner therefore
serialises (the owner's own sync, every recipient of any of that owner's shares, sibling
shares included). **Reduced parallelism is the price:** two recipients of two different
shared folders of one owner cannot sync at the same moment (the second gets 423 and floccus
retries). Different owners never interact. Revoking a credential or deactivating a user
releases the lock they held.

* `POST lock` takes or renews (TTL 5 minutes; floccus renews every 2 minutes). Another
  credential's overlapping, unexpired lock gives **423**.
* `DELETE lock` releases only the caller's own lock. If it holds none and someone else
  holds an overlapping one, the answer is 423 and nothing is released.
* Mutations are **enforced**, not advisory: while another credential holds an overlapping
  lock, writes fail with 423. Reads are never blocked.
* Credentials that are not floccus (no lock held) can still write when nobody holds a lock.

## Behaviour you should know about

* **Same URL twice in the *same* folder is not supported.** Stock floccus itself drops
  such duplicates before syncing (`filterOutDuplicatesInTheSameFolder`), so they stay
  local. Placements of one URL in *different* folders are preserved as separate placements
  of one bookmark record.
* **Delete-only same-folder duplicate resolution:** stock floccus 5.13.0 was tested in
  Chrome, Edge, Brave and Firefox. When a user moves one same-URL bookmark into a folder
  already containing another copy, floccus sends a DELETE for the old placement and does
  not tell the server whether this was a move or an intentional deletion. If that DELETE
  removes the source's last live placement and exactly one same-URL record remains visible
  to the caller, the source title and tags replace the survivor's metadata before the
  source is trashed. The survivor's id and placements stay unchanged. An intentional
  deletion of the last placement can therefore update the other copy. No merge occurs when
  the source still has placements, no survivor exists, more than one visible survivor
  exists, or a candidate is hidden from the caller. This latest-wins behavior is limited to
  the caller's visible scope.
* **Duplicate matching only considers visible records.** One record per exact URL is
  maintained within what a credential can see. If an owner holds a private bookmark X
  and a share recipient POSTs X, the recipient gets a **separate** record: it never learns,
  inherits or changes the private title, tags or placements (controller ruling). The owner
  then sees two records for X. Once a record is visible to a share, its title/URL/tags are
  shared across its known placements; updates through the share never remove or modify
  placements outside the share and `folders[]` stays filtered. **Editing the URL of an
  already-visible shared record is allowed** and, exactly like its title and tags, **affects
  every reference to that record, including placements the share cannot see** (those rows are
  kept, only the record's URL changes).
* **Same-folder URL dedupe is scoped to what the writer can see.** Within the records and
  folders visible to a credential, a folder never holds two live placements of one exact URL.
  Single-folder POSTs (all that stock floccus sends) resolve to the record already placed in
  that folder, else the lowest-id visible record, with no candidate cap. Two records that are
  **both visible** to the writer are never merged: a PUT/POST adding a placement to a folder
  already holding the other record, or a URL edit onto a URL another visible record has, is
  an explicit generic **409** that writes nothing and names no title or folder. **Hidden data
  is never consulted:** a URL present only in a folder hidden from the writer behaves exactly
  like a fresh URL (success, same response). Scoped exception: that can leave a duplicate
  inside a hidden folder; the owner sees it and resolves it in the planned duplicate-review
  feature. An explicit API PUT that adds a record to a folder already holding another visible
  same-URL record returns 409 without changing either record. In real-browser testing, stock
  floccus did not send that PUT for the native move: it sent only the DELETE described above.
  The tested native sync completed, and the latest source title/tags reached the sole visible
  survivor. Cross-scope recipient records remain separate and hidden metadata is never
  consulted.
* **Titles and URLs are never normalised.** Empty titles, leading/trailing whitespace and
  URL case/trailing slashes round-trip byte for byte (floccus hashes what it sent).
* Accepted URL schemes: `http`, `https`, `ftp`, `ftps`, `file`, `javascript`. Anything else
  (for example `chrome://`, `about:`, `data:`) is rejected with 400 and an explicit message;
  such bookmarks therefore will **not** sync. URLs are limited to 8 KiB (32 KiB for
  `javascript:`), titles to 4 KiB. `javascript:` bookmarks are stored and returned
  (`target` carries the code) but never executed or fetched.
* Deleting is a soft delete: rows keep their placements and structure so a later restore is
  possible, and trashed items are invisible to the adapter. There is no restore API and no
  30-day purge job yet.
* Tags exist server-side only; browsers cannot store them, so floccus never sends them.
* Folder depth is limited to 256 levels. Hashes are computed from current rows on every
  request, so they are never stale.

## Setting up a floccus profile (initial isolated gate)

Login Flow v2 (the floccus "Connect" wizard) will be wired to real website
authentication in the next milestone and is **not** faked here, so the wizard cannot
connect yet. The supported gate path is floccus's profile import:

1. Create a credential with the CLI (see above) and copy the secret.
2. Save a file `floccus.export.json` (a JSON array) outside any repository, with your
   own values in place of the placeholders:

   ```json
   [{
     "type": "nextcloud-bookmarks",
     "url": "http://127.0.0.1:8080/",
     "username": "alice",
     "password": "<APP-SECRET-FROM-CLI>",
     "serverRoot": "",
     "strategy": "default",
     "syncIntervalEnabled": true,
     "syncInterval": 15,
     "failsafe": true,
     "includeCredentials": false,
     "allowRedirects": false,
     "label": "BookmarksSync test"
   }]
   ```

   Use `"strategy": "slave"` ("always download") for a **view** credential or a read-only
   share. The file contains a plaintext secret: treat it as sensitive, import it, then delete it.
3. In floccus open *Options → Import/Export → Import profiles*, choose the file, then pick
   the local folder to sync when prompted (use the browser's absolute root to sync both the
   bookmarks bar and other bookmarks as separate roots).

Never commit the file or a real secret.

## Protocol test coverage (`go test ./...`)

Real temporary SQLite, no mocked store. Highlights: persistence across reopen; two-user
isolation on every route; canonical roots; cycle/self-parent/out-of-scope rejection with
no partial writes; mixed order and `childorder`; same URL in two folders and removing one;
tags preserved; unicode, HTML-sensitive, empty and padded titles; hash vectors computed by
real JS (`internal/bookmarks/testdata/genhashvectors.mjs`) covering raw U+2028/U+2029 **and**
the literal text ` `, quotes, backslashes, `<>&`, controls and root-title omission;
empty, sparse and deep (150 level) trees; malformed ids/JSON/limits/oversized bodies; bulk
import nesting, transactional failure and limits; read-only and grant revoke/downgrade;
scope-filtered `folders[]` and hidden-placement preservation; the cross-scope URL collision
(GET/POST/PUT); overlapping/renewed/expired locks and unauthorised release; unrelated private
roots independent; trash invisibility; constant-time secret verification inputs and no query
token acceptance. These prove the contract, **not** browser behaviour.

## Pending browser checks (all NOT RUN)

Acceptance scripts A1 to A14 from the audit remain pending: setup via profile import, native
roots across Chrome/Edge/Firefox/Brave, order, nesting/move/rename/delete, placements and the
same-folder limitation, lock contention (423), offline conflict, download-only read-only
share (the request log for that credential must show only `GET` and lock verbs), read-only
misconfiguration, isolation, edit-share concurrency, import file with revocation, hash
efficiency, and the 20% failsafe. The `floccus request` log lines are the intended evidence.
