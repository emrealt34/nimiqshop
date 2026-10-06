package db

import (
	"strings"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"

	adminmodel "nimiqshop/internal/admin"
	"nimiqshop/internal/nimiq"
)

// TestResetDeletesCustomersAndKeepsOperators is the test for the operator's
// "start the shop over" switch, and it exists because the two ways that switch
// can go wrong are both silent:
//
//   - It deletes too little: customer rows survive a reset, and the owner's
//     "fresh install" turns out to still hold the old accounts.
//   - It deletes too much: the operator's own account goes with them, and the
//     person who pressed the button is locked out of the console they were
//     standing in. There is no way back from that without shell access.
//
// So it does both halves: a customer is seeded and must be GONE afterwards, and
// an operator account, a site setting and a meta record are seeded and must
// still be readable afterwards. It also checks the preview BEFORE the delete,
// because the console's confirmation screen is built from that number — a
// preview of zero on a non-empty shop would tell the operator there is nothing
// to do while the button deletes everything.
func TestResetDeletesCustomersAndKeepsOperators(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	const wallet = "NQ20 TSB0 DFSM UH9C 15GQ GAGJ TTE4 D3MA 859E"
	normalized := nimiq.NormalizeAddress(wallet)
	if _, err := store.FindOrCreateUserByAddress(normalized); err != nil {
		t.Fatalf("create customer: %v", err)
	}

	// The things a reset must NOT touch: the operator identity, the settings
	// row and a plain meta record.
	if _, err := store.CreateAdminUser(adminmodel.User{
		Username:     "emrealt34",
		PasswordHash: "not-a-real-hash",
		TOTPSecret:   "not-a-real-secret",
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create operator: %v", err)
	}
	if _, err := store.SetGlobalMarginBps(100, "emrealt34", time.Now().UTC()); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := store.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("meta:notif:test-ref"), []byte(`{"sent":true}`))
	}); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	// Preview first: it is what the console shows before anything happens.
	preview, err := store.ResetPreview()
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview["u:"] < 1 {
		t.Fatalf("preview reports %d customer accounts for a shop with one customer", preview["u:"])
	}
	if total := previewTotal(preview); total < 1 {
		t.Fatalf("preview total is %d for a non-empty shop", total)
	}

	deleted, err := store.ResetShopData()
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if deleted["u:"] < 1 {
		t.Fatalf("reset claims it deleted %d customer accounts", deleted["u:"])
	}

	// The customer is gone from both the account and the lookup index.
	// The store may translate badger's sentinel into its own, so the assertion
	// is the behaviour ("no such customer"), not the exact error value.
	if _, err := store.GetUserByAddress(normalized); err == nil {
		t.Fatal("the customer's wallet still resolves after the reset")
	}
	if n, err := store.CountUsers(); err != nil || n != 0 {
		t.Fatalf("CountUsers after reset = %d (err %v), want 0", n, err)
	}

	// A second preview must now be empty of customers: the button that deletes
	// nothing must not look like a button that deletes everything.
	after, err := store.ResetPreview()
	if err != nil {
		t.Fatalf("preview after reset: %v", err)
	}
	if after["u:"] != 0 {
		t.Fatalf("customer accounts after reset = %d, want 0", after["u:"])
	}

	// The operator, the settings and the meta record all survived: the login
	// that pressed the button still works, and the shop still knows itself.
	if _, err := store.FindAdminByUsername("emrealt34"); err != nil {
		t.Fatalf("operator login was deleted by the reset: %v", err)
	}
	if s, err := store.GetAdminSettings(0); err != nil || s.UpdatedBy != "emrealt34" {
		t.Fatalf("site settings after reset = %+v (err %v)", s, err)
	}
	if err := store.View(func(txn *badger.Txn) error {
		_, err := txn.Get([]byte("meta:notif:test-ref"))
		return err
	}); err != nil {
		t.Fatalf("meta record did not survive the reset: %v", err)
	}
}

// TestWipeNamespacesNeverCoverOperatorKeyspace is the guard rail against a
// future edit that adds a prefix to the wipe table by pattern instead of by
// reading it: no wipe prefix may overlap a protected operator namespace.
// The customer-derived ratings aggregate is a deliberately deletable exact
// metadata key; it must not make every `meta:` key deletable.
func TestWipeNamespacesNeverCoverOperatorKeyspace(t *testing.T) {
	protected := []string{"ix:au:", "meta:admin:", "meta:notif:", "meta:ix:q:", "meta:fx_snapshot", "meta:rates_snapshot", "snap:cat:", "au:", "adm:", "sess:"}
	for _, ns := range wipeNamespaces {
		if ns.Label == "" {
			t.Fatalf("wipe namespace %q has no label; the console would show an unnamed row", ns.Prefix)
		}
		if ns.Prefix == "" {
			t.Fatal("a wipe namespace has an empty prefix, which would delete EVERY key in the database")
		}
		for _, keyspace := range protected {
			if strings.HasPrefix(ns.Prefix, keyspace) || strings.HasPrefix(keyspace, ns.Prefix) {
				t.Fatalf("wipe namespace %q (label %q) overlaps protected operator keyspace %q", ns.Prefix, ns.Label, keyspace)
			}
		}
	}
}

// previewTotal sums a preview so the test can assert the console's headline
// number rather than only the individual rows.
func previewTotal(preview map[string]int) int {
	total := 0
	for _, n := range preview {
		total += n
	}
	return total
}
