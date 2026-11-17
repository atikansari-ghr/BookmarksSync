# BookmarksSync sign-in UI

React + TypeScript + Vite source for the sign-in and account-security pages served
under `/auth/` (login, two-step verification, provider sign-in completion, and the
account security page: passkeys, authenticator app and recovery codes, linked
accounts, sessions, personal API tokens and Floccus connections).

The production build is written to `internal/webui/static/` and embedded into the Go
binary, so Go builds and tests never need Node. Rebuild and commit it after changing
anything here; CI fails when the committed build differs from a fresh one.

```sh
npm ci            # exact versions from package-lock.json
npm run typecheck
npm test          # component and flow tests (Vitest + Testing Library, jsdom)
npm run build     # -> ../internal/webui/static
```

Rules the code follows: same-origin API calls only; the CSRF token, flow handles and
one-time secrets live in memory only (nothing is written to localStorage or
sessionStorage, which the tests enforce); no inline scripts or styles (the server
sends a strict Content-Security-Policy).

Real-browser coverage is in `tests/browser/ui-smoke.mjs` (`npm run ui` there).
