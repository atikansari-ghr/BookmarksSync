# Connection naming verification — 2026-11-26

Login Flow v2 previously gave every connection the label `Login Flow v2
connection`. Approval now accepts a chosen name, and account Settings can rename
an existing connection without issuing another credential. Migration 0015 adds a
defaulted label column to pending flows and leaves existing credentials intact.

Stock Floccus 5.14 keeps its profile label locally. Its wizard dispatches the
login action with only the server URL; see its
[versioned source](https://github.com/floccusaddon/floccus/blob/v5.14.0/src/ui/views/NewAccount.vue).
Users must enter the same name on the website approval screen. Renaming either
side does not automatically rename the other.

| Check | Result | Evidence and limits |
| --- | --- | --- |
| Full Linux backend suite | PASS | `go test ./... -count=1`, `go vet ./...`, and `CGO_ENABLED=1 go test -race ./... -count=1` in an isolated Go 1.27.4 Debian container on the test LXC. |
| Naming and rename regressions | PASS | Exact/trimmed/Unicode names, invalid labels, omitted-label compatibility, owner-only active connections, session/CSRF/origin checks, permission and secret preservation, migration from schema 0014 with in-flight flows. |
| Web checks | PASS | 85 component tests, TypeScript check and embedded production build. |
| Real website flow and rename | PASS | Disposable Chromium profile: password login, named approval, one-time credential, real Floccus API authentication, mobile/desktop layout; Settings rename to `BookmarksSync-Home-Firefox`, owner metadata matches and original credential still authenticates. |
| Stock extension login action | PASS | Unmodified Floccus 5.13.0 Chrome opens named consent, polls and receives a usable credential with the chosen label. |
| Complete stock wizard | PASS | Unmodified Floccus 5.13.0 Chrome, visible wizard only: local profile label equals approved server connection name, credential authenticates, native bookmark and folder upload succeed. |
| Formatting | PASS | `gofmt -l cmd internal` prints nothing; `git diff --check` succeeds. |

The browser harness pins Floccus 5.13.0. Floccus 5.14's label transmission was
reviewed from source; its complete wizard and Firefox connection flow were not
driven in this change. An optional stock-extension screenshot capture timed out
on the sync-options screen. The complete functional wizard passed without that
optional capture; mandatory assertions remained enabled.

Screenshots use a disposable test account:

- [Named approval on mobile](images/connection-name-mobile.png)
- [Renamed connection in account security](images/connection-renamed-mobile.png)

Reproduce with `npm test`, `npm run typecheck` and `npm run build` in `web/`,
the Go commands above on Linux, and `node tests/browser/loginflow-smoke.mjs
--wizard` / `--stock` with the browser harness assets installed. Private runtime
logs, credentials and live database backups are excluded from Git.
