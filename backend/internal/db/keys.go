package db

import (
	"fmt"
	"strings"
)

// Badger is a flat ordered key/value store: there are no tables, no
// secondary indexes, and no server-side constraints. Everything the SQL
// schema expressed declaratively has to become an explicit key here.
//
// The layout below uses ':'-delimited prefixes so that a prefix scan
// (badger's Iterator with opts.Prefix) reproduces what a SELECT ... WHERE
// used to do:
//
//	u:<user_id>                        -> User (JSON)
//	ix:u:addr:<nimiq_address>          -> user_id      (was UNIQUE on nimiq_address)
//
//	o:<order_id>                       -> Order (JSON)
//	ix:o:user:<user_id>:<rts>:<id>     -> order_id     (was idx_orders_user)
//	ix:o:status:<status>:<id>          -> order_id     (was idx_orders_status)
//	ix:o:cr:<supplier_order_id>       -> order_id     (was the webhook's WHERE lookup)
//	ix:o:idem:<idempotency_key>        -> order_id     (was UNIQUE on idempotency_key)
//
//	q:<quote_id>                       -> Quote (JSON)   (direct-NIM purchase)
//	ix:q:user:<user_id>:<rts>:<id>     -> quote_id
//	ix:q:status:<status>:<id>          -> quote_id
//
// NOTE: nim.shop is non-custodial — there is no balance, no deposit, and no
// internal USD ledger. The old d:<deposit> / l:<ledger_entry> keyspaces and
// their indexes have been removed entirely.
//
// <rts> is a reversed timestamp (see reverseTS) so that a forward prefix
// scan yields newest-first, which is what every ORDER BY created_at DESC
// in the original queries wanted.

const (
	prefixUser          = "u:"
	prefixOrder         = "o:"
	prefixQuote         = "q:"
	prefixSupportTicket = "st:"
	prefixSupportMsg    = "stm:"
)

func quoteKey(id string) []byte { return []byte(prefixQuote + id) }
func quoteUserIndexKey(userID string, created int64, id string) []byte {
	return []byte("ix:q:user:" + userID + ":" + reverseTS(created) + ":" + id)
}
func quoteUserIndexPrefix(userID string) []byte {
	return []byte("ix:q:user:" + userID + ":")
}
func quoteStatusIndexKey(status, id string) []byte { return []byte("ix:q:status:" + status + ":" + id) }

// quoteSupplierOrderIndexKey maps a Cryptorefills order id to the local
// quote. Webhooks and the fulfillment poller use this index so delivery
// never requires a supplier listing or a full scan.
func quoteSupplierOrderIndexKey(supplierOrderID string) []byte {
	return []byte("ix:q:cro:" + supplierOrderID)
}

func quoteIdempotencyIndexKey(userID, key string) []byte {
	return []byte("ix:q:idem:" + userID + ":" + key)
}

// quoteUserLimitLockKey serializes daily-limit checks for one user. The key is
// written in the same transaction as the quote so concurrent quote attempts
// cannot both pass a read-then-write budget check (phantom protection).
func quoteUserLimitLockKey(userID string) []byte {
	return []byte("lock:q:user:" + userID)
}

func userKey(id string) []byte  { return []byte(prefixUser + id) }
func orderKey(id string) []byte { return []byte(prefixOrder + id) }

// userAddrIndexKey enforces what `nimiq_address TEXT UNIQUE` used to.
func userAddrIndexKey(addr string) []byte {
	return []byte("ix:u:addr:" + strings.ToUpper(strings.ReplaceAll(addr, " ", "")))
}

// reverseTS maps a unix-nano timestamp to a lexicographically sortable
// string that descends as time ascends, so prefix scans come back
// newest-first without having to load and sort every row in Go.
func reverseTS(unixNano int64) string {
	const maxNano = int64(1<<62 - 1)
	return fmt.Sprintf("%019d", maxNano-unixNano)
}

func orderUserIndexKey(userID string, createdUnixNano int64, id string) []byte {
	return []byte("ix:o:user:" + userID + ":" + reverseTS(createdUnixNano) + ":" + id)
}
func orderUserIndexPrefix(userID string) []byte {
	return []byte("ix:o:user:" + userID + ":")
}

func orderStatusIndexKey(status, id string) []byte {
	return []byte("ix:o:status:" + status + ":" + id)
}
func orderStatusIndexPrefix(status string) []byte {
	return []byte("ix:o:status:" + status + ":")
}

func orderSupplierIndexKey(supplierOrderID string) []byte {
	return []byte("ix:o:cr:" + supplierOrderID)
}

func orderIdempotencyIndexKey(key string) []byte {
	return []byte("ix:o:idem:" + key)
}

// Support ticket and message keys
func supportTicketKey(id string) []byte { return []byte(prefixSupportTicket + id) }
func supportTicketUserIndexKey(userID string, createdUnixNano int64, id string) []byte {
	return []byte("ix:st:user:" + userID + ":" + reverseTS(createdUnixNano) + ":" + id)
}
func supportTicketUserIndexPrefix(userID string) []byte {
	return []byte("ix:st:user:" + userID + ":")
}
func supportTicketOrderIndexKey(orderID, id string) []byte {
	return []byte("ix:st:order:" + orderID + ":" + id)
}
func supportTicketOrderIndexPrefix(orderID string) []byte {
	return []byte("ix:st:order:" + orderID + ":")
}
func supportTicketStatusIndexKey(status string, createdUnixNano int64, id string) []byte {
	return []byte("ix:st:status:" + status + ":" + reverseTS(createdUnixNano) + ":" + id)
}
func supportTicketStatusIndexPrefix(status string) []byte {
	return []byte("ix:st:status:" + status + ":")
}
func supportTicketAllIndexKey(createdUnixNano int64, id string) []byte {
	return []byte("ix:st:all:" + reverseTS(createdUnixNano) + ":" + id)
}
func supportTicketAllIndexPrefix() []byte {
	return []byte("ix:st:all:")
}

func supportMessageKey(id string) []byte { return []byte(prefixSupportMsg + id) }
func supportMessageTicketIndexKey(ticketID string, createdUnixNano int64, id string) []byte {
	return []byte(fmt.Sprintf("ix:stm:t:%s:%019d:%s", ticketID, createdUnixNano, id))
}
func supportMessageTicketIndexPrefix(ticketID string) []byte {
	return []byte("ix:stm:t:" + ticketID + ":")
}

/* ---------------- Public activity feed ----------------
 *
 * Newest-first stream of public purchases. Each kind gets its own prefix so a
 * prefix scan yields that kind's most recent items; the handler merges them.
 *
 *	ix:feed:o:<rts>:<order_id>  a delivered purchase (gift card / top-up / eSIM)
 *	ix:feed:q:<rts>:<quote_id>  a fulfilled direct-NIM payment
 *
 * <rts> is reverseTS(timestamp) so ascending key order is newest-first.
 */
func feedOrderIndexKey(whenUnixNano int64, id string) []byte {
	return []byte("ix:feed:o:" + reverseTS(whenUnixNano) + ":" + id)
}
func feedOrderIndexPrefix() []byte { return []byte("ix:feed:o:") }

func feedQuoteIndexKey(whenUnixNano int64, id string) []byte {
	return []byte("ix:feed:q:" + reverseTS(whenUnixNano) + ":" + id)
}
func feedQuoteIndexPrefix() []byte { return []byte("ix:feed:q:") }

// metaRatingAggregateKey is the single key holding the global RatingAggregate.
const metaRatingAggregateKey = "meta:rating_aggregate"
