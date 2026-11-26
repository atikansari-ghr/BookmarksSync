# Preserve user-selected Floccus connection names

User requirement: a Floccus profile named `BookmarksSync-Home-Firefox` should be
identifiable by the same name in BookmarksSync Settings. The current Login Flow
implementation hardcodes every resulting credential label.

Stock Floccus's wizard keeps the profile label locally and dispatches Login Flow
with only the server URL. The server cannot infer that private client-side value.
Use a named approval and owner-only rename, without modifying the extension or
changing credentials, permissions, bookmark data or sync state. The user has opted
out of approval questions; implement this bounded naming fix directly.

## Interface and behavior

- Approval accepts optional `label`; omission retains the legacy default for old
  callers. Explicit names are trimmed, nonempty and at most 200 Unicode runes.
  Persist the approved name in the flow and use it when polling mints a credential.
- Add migration 0015 with a label column on pending flows; retain existing data.
- `PATCH /api/v1/me/connections/{id}` accepts `{label}` and renames only an active
  connection owned by the signed-in user. Return its metadata in `{connection}`;
  preserve the ID, secret hash, mode, kind and share. Other owners/revoked IDs get
  404; ordinary session/CSRF protections apply. No credential reissuance.
- Approval UI asks for a connection name and explains using the Floccus profile
  label. Existing connections have Rename with Save/Cancel and visible errors.
- No automatic name synchronization is claimed when Floccus changes a local label.

## Work

- [x] Backend: regression tests, migration, identity, approval/API and OpenAPI.
- [x] Frontend: approval and rename controls with component regressions; update
  consent/wizard smoke to fill and verify exact labels.
- [x] Independent verification: Go tests/vet/race, web checks/build, real default and
  stock wizard flows, metadata/secret preservation, ownership failures.
- [ ] Commit/push to the existing branch, verify CI, back up the live LXC before
  applying the verified change, and report the exact stock-client limitation.

Validation must use disposable profiles/accounts. Keep real credentials and
bookmark exports outside Git. A live update must preserve the deployed `.env`,
volumes, user data and active sync credentials.
