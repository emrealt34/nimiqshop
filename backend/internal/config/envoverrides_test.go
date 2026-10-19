package config

import (
	"os"
	"strings"
	"testing"
)

/* envoverrides_test.go — the rules behind "add an env key from the admin
 * page". Two things are pinned here:
 *
 *   1. a value that would take the checkout down is REFUSED AT SAVE TIME, with
 *      the reason (the same rule config.Validate applies at boot — a partner
 *      key that is not a partner key is not a warning, every order would 401);
 *   2. a bad stored row can never stop the server. It is skipped with a
 *      warning and the host value keeps standing, because the console is the
 *      only way back in once a value has been saved.
 */

func TestAValueThatWouldTakeTheCheckoutDownIsRefused(t *testing.T) {
	cases := []struct {
		key, value string
	}{
		{"CRYPTOREFILLS_PARTNER_KEY", "v5-user-session-key"},        // not a partner key
		{"CRYPTOREFILLS_PARTNER_KEY_BACKUP", "cr_live_other_prefix"}, // wrong prefix
		{"CRYPTOREFILLS_BASE_URL", "http://api.example.com"},         // plain http
		{"SITE_HOST", "shop example.com"},                            // not a hostname
		{"DAILY_ORDER_LIMIT", "-4"},
		{"DAILY_SPEND_LIMIT_USD", "not-a-number"},
		{"SITE_HOST", "  "},
	}
	for _, tc := range cases {
		if err := ValidateEnvValue(tc.key, tc.value); err == nil {
			t.Fatalf("%s = %q must be refused", tc.key, tc.value)
		}
	}
	for _, good := range []struct{ key, value string }{
		{"CRYPTOREFILLS_PARTNER_KEY", "cr_pk_5m857U5H6kC6dMeyMu01Le1ffK-rggAzh3n3hVeFZTo"},
		{"CRYPTOREFILLS_BASE_URL", "https://api.cryptorefills.com"},
		{"SITE_HOST", "shop.example.com"},
		{"DAILY_ORDER_LIMIT", "0"},
		{"MONTHLY_SPEND_LIMIT_USD", "1000"},
	} {
		if err := ValidateEnvValue(good.key, good.value); err != nil {
			t.Fatalf("%s = %q must be accepted: %v", good.key, good.value, err)
		}
	}
}

func TestTheConsoleMayNotTouchWhatHoldsTheProcessUp(t *testing.T) {
	for _, key := range []string{"JWT_SECRET", "BADGER_DIR", "LISTEN_ADDR", "PORT", "STATIC_DIR", "ADMIN_SESSION_SECRET"} {
		if err := ValidateEnvKey(key); err == nil {
			t.Fatalf("%s must belong to the host, not the console", key)
		}
	}
	for _, key := range []string{"CRYPTOREFILLS_PARTNER_KEY", "SITE_HOST", "MY_OWN_KNOB"} {
		if err := ValidateEnvKey(key); err != nil {
			t.Fatalf("%s must be manageable: %v", key, err)
		}
	}
	for _, key := range []string{"lower_case", "with space", "X", ""} {
		if err := ValidateEnvKey(key); err == nil {
			t.Fatalf("%q is not a variable name and must be refused", key)
		}
	}
}

func TestAPartnerKeySavedInTheConsoleIsInForceWithoutARestart(t *testing.T) {
	const hostKey = "cr_pk_host"
	t.Setenv("CRYPTOREFILLS_PARTNER_KEY", hostKey)
	cfg := Config{CRPartnerKey: hostKey, SiteHost: "shop.test"}

	cfg, warnings := ApplyEnvOverrides(cfg, map[string]string{
		"CRYPTOREFILLS_PARTNER_KEY": "cr_pk_from_the_console",
		"SITE_HOST":                 "console.example.com",
	})
	if len(warnings) != 0 {
		t.Fatalf("a clean override must not warn: %v", warnings)
	}
	if cfg.CRPartnerKey != "cr_pk_from_the_console" {
		t.Fatalf("the live client's key = %q, want the console value", cfg.CRPartnerKey)
	}
	if cfg.SiteHost != "console.example.com" {
		t.Fatalf("SiteHost = %q, want the console value", cfg.SiteHost)
	}
	// The value is exported too, so anything that reads the environment later
	// (a library, a reload) sees the operator's value and not the host's.
	if got := os.Getenv("CRYPTOREFILLS_PARTNER_KEY"); got != "cr_pk_from_the_console" {
		t.Fatalf("os env = %q, want the console value", got)
	}
	t.Cleanup(func() { _ = os.Setenv("CRYPTOREFILLS_PARTNER_KEY", hostKey) })
}

func TestABadStoredRowSkipsInsteadOfStoppingTheServer(t *testing.T) {
	t.Setenv("CRYPTOREFILLS_PARTNER_KEY", "cr_pk_host")
	cfg := Config{CRPartnerKey: "cr_pk_host"}
	t.Cleanup(func() { _ = os.Setenv("CRYPTOREFILLS_PARTNER_KEY", "cr_pk_host") })

	cfg, warnings := ApplyEnvOverrides(cfg, map[string]string{
		"CRYPTOREFILLS_PARTNER_KEY": "not-a-partner-key", // refused
		"BADGER_DIR":                "/tmp/elsewhere",    // belongs to the host
		"SOMETHING_UNKNOWN":         "value",             // allowed, but worth saying
	})
	if len(warnings) != 3 {
		t.Fatalf("every bad row must be reported, got %v", warnings)
	}
	// The host value is untouched — the server starts exactly as it would
	// have without the console rows.
	if cfg.CRPartnerKey != "cr_pk_host" {
		t.Fatalf("CRPartnerKey = %q, the host value must survive a bad row", cfg.CRPartnerKey)
	}
	if got := os.Getenv("CRYPTOREFILLS_PARTNER_KEY"); got != "cr_pk_host" {
		t.Fatalf("os env = %q, a refused value must not be exported", got)
	}
}

func TestSecretsAreNeverShownWhole(t *testing.T) {
	const key = "cr_pk_5m857U5H6kC6dMeyMu01Le1ffK-rggAzh3n3hVeFZTo"
	masked := MaskValue(key, true)
	if strings.Contains(masked, "5m857U5H6kC6dMeyMu01Le1ffK") {
		t.Fatalf("the secret is readable in the mask: %s", masked)
	}
	if !strings.HasPrefix(masked, "cr_pk_") {
		t.Fatalf("the mask must still say WHICH kind of key it is: %s", masked)
	}
	// A value short enough that masking would be theatre shows dots.
	if dots := MaskValue("short", true); strings.Contains(dots, "short") {
		t.Fatalf("a short secret must not be shown: %s", dots)
	}
	// A value that is not a secret is shown: the operator asked for it.
	if got := MaskValue("shop.example.com", false); got != "shop.example.com" {
		t.Fatalf("a non-secret must be readable, got %s", got)
	}
	if got := MaskValue("", true); got != "" {
		t.Fatalf("an unset value has nothing to show, got %s", got)
	}
}

func TestEveryManagedKeyHasAnHonestDescription(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range ManagedEnvKeys {
		if seen[spec.Key] {
			t.Fatalf("%s is listed twice", spec.Key)
		}
		seen[spec.Key] = true
		if err := ValidateEnvKey(spec.Key); err != nil {
			t.Fatalf("%s is offered by the console but refused as a name: %v", spec.Key, err)
		}
		if spec.Label == "" || spec.Hint == "" {
			t.Fatalf("%s has no label/hint for the operator", spec.Key)
		}
		if spec.Live && spec.Key != "CRYPTOREFILLS_PARTNER_KEY" && spec.Key != "CRYPTOREFILLS_PARTNER_KEY_BACKUP" {
			t.Fatalf("%s claims to be live but nothing applies it to a running client", spec.Key)
		}
	}
	if _, ok := ManagedEnvSpec("CRYPTOREFILLS_PARTNER_KEY"); !ok {
		t.Fatal("the partner key must be a managed, live key — it is the one the owner named")
	}
	if _, ok := ManagedEnvSpec("NOT_A_KEY"); ok {
		t.Fatal("an unknown key must not be reported as managed")
	}
}
