# auth

Read when: pairing a store, checking auth state, logging out, or choosing QR vs phone pairing.

`whatscli auth` connects interactively and bootstraps sync after successful pairing. `whatscli sync` never shows a QR code, so use `auth` first for a new store or named account.

## Commands

```bash
whatscli auth [--follow] [--idle-exit 30s] [--download-media] [--qr-format terminal|text] [--phone PHONE] [--events]
whatscli auth status
whatscli auth logout
whatscli --account work auth status
```

## Notes

- Default pairing prints a terminal QR code.
- `--qr-format text` prints the raw QR payload for external renderers.
- `--phone PHONE` uses WhatsApp phone-number pairing instead of QR pairing.
- Transient websocket drops before pairing completes are retried with a fresh QR/code.
- Passkey-gated pairing is not yet supported. If WhatsApp requests passkey verification or confirmation, auth stops with an actionable error instead of continuing to rotate unusable QR codes.
- The pinned SDK exposes passkey/WebAuthn response and confirmation primitives; WhatsCLI has no authenticator integration. See [the tested integration boundary](continuous-acceptance.md#sdk-boundaries); a refusal does not authorize clearing an existing store.
- After pairing, auth waits for confirmed login before starting metadata bootstrap, then syncs until idle unless `--follow` is set. Pairing success alone does not confirm login; cancellation, login rejection or revocation stop this wait. An opened transport retains the bounded final presence cleanup even when the login wait fails.
- Bootstrap sync honors `WACLI_SYNC_MAX_MESSAGES` and `WACLI_SYNC_MAX_DB_SIZE` to cap local history growth.
- `--events` emits NDJSON lifecycle events on stderr, including raw QR and phone-pairing codes for external renderers.
- `auth status` reads the existing session read-only by default and creates no store, session, or writer lock. A missing session reports unauthenticated. It reports whether the local store is authenticated. A recorded remote logout overrides a stale device row until WhatsApp confirms a new login.
- `auth status` and offline `doctor` use normal SQLite readonly locking. WAL/SHM bookkeeping may be created or updated even when the session starts without sidecars; required permission failures are not retried as immutable reads. See [store readonly access](store.md#local-reads-by-default) for the no-create and live-read limits.
- Legacy offline `doctor` keeps diagnostic exit 0 and reports an unreadable auth source in `store_error`. Its existing `authenticated=false` alongside that auth-source error does not prove logout or loss of authentication; `linked_jid` is omitted. Known auth/JID data is retained when only the archive fails. An absent session remains normally unauthenticated. Agent doctor reports a sanitized error with exit 4 and no auth data on a failed read.
- `auth logout` invalidates the linked-device session and requires writable mode and the selected store's writer lock. If a remote logout is already recorded (`session_revoked=true`), it removes the retained local SDK device/session data without connecting. It preserves the account registration, archive, drafts/outbound records, receipts and change cursors/checkpoints. The revocation marker remains until WhatsApp confirms a new login; successful cleanup does not mean authenticated.
- For multiple accounts, prefer `whatscli accounts add NAME`; it creates an isolated account store and runs the same auth/bootstrap flow.

## Recover a revoked session

Use the existing account explicitly for both commands:

```bash
whatscli -a work auth logout
whatscli -a work auth
# Or pair by phone: whatscli -a work auth --phone "+1234567890"
```

For a manual store, use the same `--store DIR` instead of `-a work`. Do not add the existing account again or delete database files. If logout first discovers a remote revocation while connecting, it reports the error and recovery guidance; rerun `auth logout` with the same selection to clean up using the recorded revocation, then run `auth`. Ordinary connection or remote logout failures do not trigger local cleanup. An absent session without recorded revocation still reports unauthenticated.

## Examples

```bash
whatscli auth
whatscli auth --qr-format text
whatscli auth --phone "+1 (234) 567-8900"
whatscli auth --download-media
whatscli auth status --json
whatscli auth logout
```
