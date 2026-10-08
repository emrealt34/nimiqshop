package db

/*
 * wipe.go — the operator's "start over" switch.
 *
 * WHY IT EXISTS. Setting a shop up again (a new deployment, a demo, a handover)
 * used to mean either destroying the Railway volume — which also throws away
 * the operator login and every setting — or writing a throwaway script against
 * the live database. Neither is something an operator should have to do, and
 * the second one is how a production database gets damaged by a typo.
 *
 * WHAT IT DOES. One call deletes every record that belongs to CUSTOMERS: their
 * accounts and presence, their orders and quotes, their support threads, their
 * stake watch/ledger rows, their cashback rows and claim codes, the public
 * activity feed, and the per-customer checkout locks. What it deliberately
 * KEEPS: the operator accounts, the site settings and meta records, and the
 * catalog snapshots — the things that are the SHOP rather than its customers,
 * and the things that would lock the operator out of the console they are
 * standing in.
 *
 * HOW IT DELETES. Badger's DropPrefix, which writes delete markers and lets the
 * background compaction reclaim the space — far cheaper and far safer than a
 * read-and-delete walk over every key. Prefixes are listed with the label the
 * console shows, so the preview and the delete can never disagree about what is
 * about to go: both iterate the same table.
 */

// wipeNamespace is one deletable namespace: the key prefix and what it holds, in
// the words the console shows the operator.
type wipeNamespace struct {
	Label  string
	Prefix string
}

// wipeNamespaces is the complete list of customer-owned namespaces. Order is
// display order in the preview; it has no effect on the delete.
var wipeNamespaces = []wipeNamespace{
	{"Customer accounts", "u:"},
	{"Customer address index", "ix:u:addr:"},
	{"Ratings Aggregate", "meta:rating_aggregate"},
	{"Rating comments", "meta:ratings_"},
	{"Rating proofs", "ratingtx:"},
	{"Orders", "o:"},
	{"Order indexes", "ix:o:"},
	{"Quotes (checkouts)", "q:"},
	{"Quote indexes", "ix:q:"},
	{"Support tickets", "st:"},
	{"Support messages", "stm:"},
	{"Support indexes", "ix:st:"},
	{"Support message indexes", "ix:stm:"},
	{"Stake watch records", "sw:"},
	{"Stake ledger rows", "sl:"},
	{"Stake recheck queue", "sr:"},
	{"Cashback rows", "cb:"},
	{"Cashback indexes", "ix:cb:"},
	{"Public activity feed", "ix:feed:"},
	{"Checkout locks", "lock:q:"},
	{"Wallet notification opt-outs", "notifpref:"},
	{"Wallet notification ledger", "notifsent:"},
}

// ResetPreview is what the console shows BEFORE anything is deleted: how many
// records each namespace currently holds. Counting is a prefix walk, which is
// exactly what the console's other views already do.
func (s *Store) ResetPreview() (map[string]int, error) {
	out := make(map[string]int, len(wipeNamespaces))
	for _, ns := range wipeNamespaces {
		n, err := s.countPrefix([]byte(ns.Prefix))
		if err != nil {
			return out, err
		}
		if n > 0 {
			out[ns.Prefix] = n
		}
	}
	return out, nil
}

// ResetShopData deletes every customer-owned namespace and returns what was
// there, in the console's labels. A namespace that fails to drop is reported by
// name rather than silently skipped: a partial reset the operator believes was
// complete is worse than a failed one.
func (s *Store) ResetShopData() (map[string]int, error) {
	counts, err := s.ResetPreview()
	if err != nil {
		return counts, err
	}
	prefixes := make([][]byte, 0, len(wipeNamespaces))
	for _, ns := range wipeNamespaces {
		prefixes = append(prefixes, []byte(ns.Prefix))
	}
	if err := s.db.DropPrefix(prefixes...); err != nil {
		return counts, err
	}
	return counts, nil
}

// WipeNamespaces exposes the labels/prefixes for the HTTP layer's response, so
// the console can say what it removed in the same words the preview used.
func WipeNamespaces() [][2]string {
	out := make([][2]string, 0, len(wipeNamespaces))
	for _, ns := range wipeNamespaces {
		out = append(out, [2]string{ns.Prefix, ns.Label})
	}
	return out
}
