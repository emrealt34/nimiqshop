# Order data retention

Order and quote records, customer ownership, delivery payloads, payment identifiers,
review content and cashback obligations remain on the Railway `/data` volume.
Badger defaults to synchronous writes. The existing cashback recovery queue is
unchanged by these additions.

## Payment identifiers

The Mini App saves a received spending transaction hash and swap ID to an
account-scoped browser outbox before attempting the authenticated metadata POST.
The outbox contains neither credentials nor the invoice. Entries have no expiry.
Successful durable acknowledgements remove only the exact acknowledged entry;
conflicts remain available for inspection. Online, session and visibility events
resume pending writes. There is no timed retry loop. Each pending entry gets at most three attempts
per page lifecycle, triggered by a save, connection, session or visibility event.
Exhausted entries remain durable for a subsequent reload; they are not discarded. Different accounts do not
upload one another's pending records.

The server commits metadata independently of RPC availability. This endpoint
does not submit transactions, retry an invoice, or mark Lightning as settled.
Public proof discovery continues to verify the invoice-bound chain receipt.

## Reviews and schema changes

Submitted pending reviews retain their stars, comment and known transaction hash
beyond the former 15-minute expiry. Authenticated pending reviews also have a
browser copy that survives tab closure. A retry confirms the existing payment
instead of opening another wallet payment. Each automatic review check is limited
to three chain-verification requests; the stored stars and comment come from the
verified 1-Luna transaction memo, not an unverified browser payload. The backend acknowledges an identical,
already verified review for its owner without counting another edit.

Every accepted review revision is archived under `rating:history:<transaction>`
in the same transaction as the purchase, transaction-use guard and aggregate.
The previous legacy revision is archived before its first subsequent edit.
Public comment-list limits and moderation do not delete this private archive.

Typed updates preserve unknown top-level fields in original order and quote JSON,
including legacy wallet result extensions. Known fields can still intentionally
be cleared; omitted fields are not resurrected. Public views remain allowlisted.
Optional absent chain details and swap IDs are not displayed as fabricated values.

## Scope

This protects received data through ordinary connection failures, acknowledgement
loss, review edits, schema updates and process restarts. It cannot reconstruct a
wallet response never received by the shop, restore cleared browser storage, or
replace an independent backup after physical volume loss or deliberate deletion.
The operator's payment inspection reads current raw records without changing
purchase or payment state.
