package db

import (
	"errors"
	"testing"
)

/* envsettings_test.go — the store behind the console's environment editor.
 *
 * The promises are small and they are all about the operator never getting
 * stuck: a saved value comes back, a removed one is gone, a name that is not
 * a variable name is refused before it can pollute the keyspace, and removing
 * something that was never saved is not an error — the console lists host
 * variables too, and "remove" on one of those simply means "stop overriding".
 */
func TestConsoleEnvOverridesRoundTrip(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = store.Close() }()

	if rows, err := store.ListEnvOverrides(); err != nil || len(rows) != 0 {
		t.Fatalf("a fresh store has no overrides: %v %v", rows, err)
	}

	if err := store.SetEnvOverride(EnvOverride{Key: "CRYPTOREFILLS_PARTNER_KEY", Value: "cr_pk_one"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := store.SetEnvOverride(EnvOverride{Key: "SITE_HOST", Value: "shop.example.com"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Saving the same name again replaces the value — there is one row per
	// variable, never a list of historical values.
	if err := store.SetEnvOverride(EnvOverride{Key: "CRYPTOREFILLS_PARTNER_KEY", Value: "cr_pk_two"}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	rows, err := store.ListEnvOverrides()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (one per variable)", len(rows))
	}
	// Sorted by name, so the console never reshuffles itself.
	if rows[0].Key != "CRYPTOREFILLS_PARTNER_KEY" || rows[1].Key != "SITE_HOST" {
		t.Fatalf("rows are not sorted by name: %v", rows)
	}
	if rows[0].Value != "cr_pk_two" {
		t.Fatalf("value = %q, want the replaced one", rows[0].Value)
	}

	one, err := store.GetEnvOverride("SITE_HOST")
	if err != nil || one.Value != "shop.example.com" {
		t.Fatalf("get = %q, %v", one.Value, err)
	}
	if _, err := store.GetEnvOverride("NEVER_SAVED"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unsaved override must report ErrNotFound, got %v", err)
	}

	if err := store.DeleteEnvOverride("SITE_HOST"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetEnvOverride("SITE_HOST"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted override must be gone, got %v", err)
	}
	// Deleting what was never stored is a no-op, not a failure.
	if err := store.DeleteEnvOverride("SITE_HOST"); err != nil {
		t.Fatalf("deleting twice: %v", err)
	}
}

func TestConsoleEnvOverridesRefuseANameThatIsNotAVariable(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = store.Close() }()

	// These would become Badger keys; a name that is not SCREAMING_SNAKE is
	// refused here so the keyspace stays predictable.
	for _, bad := range []string{"", "partner key", "lower_case", "2FAST", "WITH-DASH"} {
		if err := store.SetEnvOverride(EnvOverride{Key: bad, Value: "x"}); err == nil {
			t.Fatalf("SetEnvOverride(%q) must be refused", bad)
		}
		if err := store.DeleteEnvOverride(bad); err == nil {
			t.Fatalf("DeleteEnvOverride(%q) must be refused", bad)
		}
	}
	// No empty value: clearing a variable is REMOVE, not "" (an empty
	// override would read as "the host set nothing" everywhere downstream).
	if err := store.SetEnvOverride(EnvOverride{Key: "SITE_HOST", Value: "   "}); err == nil {
		t.Fatal("an empty value must be refused")
	}
	if err := store.SetEnvOverride(EnvOverride{Key: "SITE_HOST", Value: "one\ntwo"}); err == nil {
		t.Fatal("a multi-line value must be refused")
	}
	if rows, err := store.ListEnvOverrides(); err != nil || len(rows) != 0 {
		t.Fatalf("nothing may be stored by a refusal: %v %v", rows, err)
	}
}
