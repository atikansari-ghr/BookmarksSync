# Backup Encryption Implementation Plan

> **For agentic workers:** Use the TDD workflow task-by-task. Steps use checkbox syntax.

**Goal:** Encrypt destination backup files independently with strong authenticated encryption while preserving an operator-held recovery key outside every archive.

**Architecture:** Keep the existing verified compressed ZIP as the source artifact. For each encrypted destination, stream it through a chunked AES-256-GCM container using a random per-destination key stored in a private `backup-keys/` directory excluded from archive contents. Admins explicitly export the hex recovery key after recent authentication; CLI verify, restore, and user recovery accept that key file. Encryption defaults off and can be changed per destination; key files remain when encryption is disabled or a destination is removed.

**Tech Stack:** Go standard crypto/cipher, SQLite migration, existing backup manager/API, React/TypeScript admin panel.

**Spec:** `docs/design.md` Operations and `docs/backup-restore.md`.

## Global Constraints

- Per-destination encryption defaults off; unencrypted existing backups stay readable.
- Recovery keys are random 256-bit values, private on disk, never embedded in backups or ordinary API snapshots/logs.
- Export requires recent admin authentication; key file download is explicit.
- Encrypted archives are streamed with bounded memory, authenticated per chunk, and use `.zip.enc` names.
- Never delete key files automatically; deleting/disabling a destination must not silently destroy archive recovery material.

## Review Focus

- Wrong key, bit flips, truncation, malformed headers, and cancellation fail without publishing plaintext.
- Key files are private and excluded from full-system backup; export is recent-auth gated.
- Encrypted and plaintext destinations can run in the same batch and retention handles each name.
- Large archives do not require loading the whole file into memory.
- Toggling encryption off preserves old recovery keys and does not transform existing files.

---

### Task 1: Key store and authenticated streaming container

**Files:** `internal/backup/encryption.go`, tests, `internal/backup` key-store helper.

- [x] Add failing tests for chunked encrypt/decrypt, wrong key, tampering, truncation, cancellation, and private random key export.
- [x] Verify RED, then implement the versioned stream envelope and key store.
- [x] Add a CLI encrypted archive option to `backup verify`, `restore`, and `restore-user`; keep legacy archive behavior.

### Task 2: Destination orchestration and protected key export

**Files:** migration `0010_backup_destination_encryption.sql`, backup Store/Manager, API backup types/routes/tests, OpenAPI contract/tests.

- [x] Test persistence and plaintext default for the destination flag.
- [x] Test mixed encrypted/plain destination publication and encrypted retention/deletion naming.
- [x] Add recent-authenticated recovery-key export and ensure no key appears in snapshots or logs.

### Task 3: Admin controls, docs, and acceptance

**Files:** `web/src/components/AdminBackups.tsx` and tests; `docs/backup-restore.md`, `README.md`, `docs/implementation-plan.md`.

- [x] Add per-destination encryption controls and explicit recovery-key download guidance.
- [x] Document restore workflow and key-loss implications.
- [x] Run Go/web suites, vet, typecheck/build, Linux/Windows builds, and isolated LXC deployment/health smoke. Encrypted archive creation/verification/restore is covered by Go integration tests; the LXC service smoke confirmed migration startup and served the new embedded UI.
- [ ] Commit the finished feature with Atik Ansari as sole author; do not push.
