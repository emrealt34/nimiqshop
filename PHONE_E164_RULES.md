# Phone numbers & `beneficiary_account` rules

How this shop handles the supplier's delivery target (`beneficiary_account`)
for every product kind — implemented so a top-up can never go to a wrong or
malformed number.

## The supplier rule (CryptoRefills)

Each delivery carries a `beneficiary_account`; that is where Cryptorefills
delivers the product. The format depends on the product kind:

| Product kind            | `beneficiary_account` must be            | Example           |
| ----------------------- | ---------------------------------------- | ----------------- |
| Gift cards & eSIMs      | the end-user's **email address**         | `user@example.com`|
| Mobile top-ups          | the recipient's **phone in E.164** — leading `+`, country code, subscriber number (8–15 digits total) | `+14155551234`, `+905551234567` |

The supplier rejects mismatches with `INVALID_BENEFICIARY_ACCOUNT`
(a phone for a gift card, or a malformed phone for a top-up).

## Where it is enforced

1. **`internal/phone`** (single source of truth)
   - `phone.Normalize(raw, countryISO)` converts customer-typed input to
     strict E.164:
     - separators (`space . - ( ) /`) are stripped,
     - a leading `+` is accepted (already international),
     - `00…` (international access prefix) becomes `+…`,
     - `0…` (national trunk prefix, e.g. `0555 123 45 67`) becomes
       `+<country code of the product>…` using a conservative dial-code
       table (`dialCodes`). Italy keeps its leading 0 (`+39 06…`).
     - **Fail-closed:** a bare number with no `+`, no `00` and no leading
       `0` is *rejected* — the shop never guesses which country a number
       belongs to, because a wrong guess means delivery to someone else.
       Countries with special access prefixes (MX `01`, RU/KZ `8`, NANPA)
       are intentionally absent from the table for the same reason.
   - `phone.Validate(s)` enforces strict E.164: `+`, first digit 1–9,
     8–15 digits total (ITU E.164 minimum is 8 digits).

2. **Every entry point normalizes before anything is stored or sent**
   - `POST /api/quotes` (`CreateQuote`), `POST /api/test-buy` (`TestBuy`)
     and `GET /api/catalog/check-phone` (`CheckPhone`).
   - Top-ups **require** a phone number; gift cards/eSIMs **never** use it
     as the delivery target (eSIMs are kind `mobile_recharge` but category
     `e-sim` and deliver to email, as do all gift cards).
   - The product's supplier `delivery_type` (`by_phone`) is an extra
     signal: `isTopUpProduct()` treats a product as a top-up when
     `delivery_type == "by_phone"` **or** (`kind == mobile_recharge` and
     category ≠ `e-sim`).

3. **The beneficiary is persisted and re-sent unchanged**
   - `db.Quote.BeneficiaryAccount` stores the exact value sent to the
     supplier (email or normalized phone) at quote creation.
   - Crash recovery (`settlement.OrderTracker.redeliverCreation`) re-sends
     that persisted value — it does **not** re-derive it ("phone if
     present"), which previously could have delivered a gift card to a
     phone number.

4. **Request shape** (`cryptorefills.CreateOrderRequest`)
   - Flat `beneficiary_account` at the delivery level, exactly as in the
     official docs' `POST /v5/orders` / `POST /v5/orders/validations`
     examples (plus top-level `email` / `user.email` for the end user).
   - Pinned by `TestCreateOrderRequestBeneficiaryShape`
     (`internal/cryptorefills/client_test.go`) so a struct change can never
     silently break deliveries again.

5. **Mock supplier is strict** (`cmd/mockstack`)
   - Parses the flat request shape (nested `deliverable` still accepted for
     leniency) and enforces the same per-kind beneficiary rules at
     `validations` *and* `orders` creation, returning the supplier's real
     error code `INVALID_BENEFICIARY_ACCOUNT`.

## End-to-end proof

`crashtest.TestC09_BeneficiaryRules` drives the real server binary against
the mock supplier and verifies, with money-free test products:

- `0014155551234` and `+90 555 123 45 67` are normalized to `+14155551234`
  / `+905551234567` by `check-phone` and by the quote path;
- a top-up quote with a customer-typed `00…` number is fulfilled with
  “Top-up completed for +14155551234”;
- a gift card quote that *also* carries a phone number is delivered to the
  **email**;
- email-as-phone, too-short/too-long numbers, bare national numbers and
  missing phones are all rejected with a 400 **before** any supplier order
  exists.

Run it with:

```sh
cd backend
go test ./crashtest/ -run '^TestC09_BeneficiaryRules$' -count=1 -v
# or the whole isolated suite:
./run-crashtests.sh
```

## Frontend

`frontend/js/validate.js` is a **1:1 JavaScript port** of `internal/phone`
(same separator rules, same `dialCodes` table, same Italy-keeps-zero rule,
same fail-closed behavior and the same error messages) plus a strict email
check matching the backend's severity. `askPhone` in
`frontend/js/pages/product.js` normalizes the customer's input locally with
those rules **before** anything is sent, then still calls
`GET /api/catalog/check-phone` for the live supplier check; the quote
endpoint re-normalizes/re-validates with the Go implementation anyway.

That makes validation equally strict on both sides: the UI refuses anything
the server would reject (defense in depth), and the server remains the
single source of truth. Parity is exercised by the case tables in
`internal/phone/phone_test.go` — running the same inputs through
`normalizePhone` (JS) produces identical results.
