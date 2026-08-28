# Admin authentication implementation contract

The admin console is a separate authentication domain. Customer Nimiq Hub JWTs
are **never** accepted by `/api/admin/*`; the protected API accepts only an
opaque `nimiqshop_admin_session` cookie.

## Bootstrap and credentials

An initial admin can be created once in either way:

1. Startup seed: set `ADMIN_USERNAME`, a pre-generated Argon2id PHC
   `ADMIN_PASSWORD_HASH`, and base32 `ADMIN_TOTP_SECRET` together. The Badger
   `meta:admin:bootstrapped` marker makes this one-time across restarts.
2. Deliberate emergency/bootstrap path: configure a 32+ byte
   `ADMIN_BOOTSTRAP_TOKEN`, then call `POST /api/admin/auth/bootstrap` with
   that token in `X-Admin-Bootstrap-Token` and `{ username, password_hash,
   totp_secret }`. It accepts hash material—not a raw password—and is closed
   permanently after the first success.

`ADMIN_SESSION_SECRET` is a separate 32+ byte secret. The server generates a
random 32-byte session credential but stores only its HMAC-SHA-256 with that
secret in Badger. It sends the credential in an `HttpOnly`, `Secure`,
`SameSite=Strict`, `/api/admin` cookie. `ADMIN_COOKIE_SECURE=false` exists
only for explicit HTTP localhost development.

## Login safeguards

- Passwords are verified against bounded-parameter Argon2id PHC records.
- TOTP is RFC 6238, six digits, 30-second period, with one adjacent clock-skew
  window in either direction.
- Five failed attempts within 15 minutes lock the canonical username for 30
  minutes. Locked attempts do not extend that window.
- Login failure, lockout, successful login, logout, bootstrap and sensitive
  settings actions append an immutable audit event with remote IP and
  user-agent. Passwords, TOTP values and raw cookies never enter an audit
  record.

## Protected API

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/api/admin/dashboard` | user count, settlement queues, treasury status, persisted margin |
| `GET` | `/api/admin/users`, `/api/admin/users/{id}` | customer directory and wallet/deposit/order/quote/ledger detail |
| `GET` | `/api/admin/orders`, `/api/admin/quotes`, `/api/admin/transactions` | cross-customer operations lists |
| `GET` | `/api/admin/manual-review` | quotes marked for manual review |
| `GET` | `/api/admin/oracle` | independent-source oracle health and spread |
| `POST` | `/api/admin/settings/margin` | persist `global_margin_bps` (0–5000) for new quotes |
| `GET` | `/api/admin/audit` | immutable audit trail |

The Astro admin UI sends all of these requests with `credentials: 'include'`;
it never reads or stores an admin credential in browser JavaScript.
