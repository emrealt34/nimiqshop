package config

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ManagedEnv describes one environment variable the operator may set from the
// admin console instead of editing the host's dashboard and redeploying.
//
// Owner (2026-10-19): "env anahtarlarını admin sayfasından ekleyip
// çıkarabilmeliyim. öyle her seferinde birşey ekliyeceksek kod değiştirelim
// gerek yok. mesela partner key gibi." So this is a real add/remove/edit
// surface, not a read-only display of what the host happens to have set.
type ManagedEnv struct {
	Key string `json:"key"`
	// Label/Hint are what the console shows; Hint says what breaks without it.
	Label  string `json:"label"`
	Hint   string `json:"hint"`
	Secret bool   `json:"secret"`
	// Live is TRUE only when saving the value takes effect in the running
	// process immediately (the supplier keys are swapped into the live
	// client). A false value is not a defect: the value is still exported
	// into the process environment and re-applied at boot, so it is in force
	// from the next start on — and the console says so instead of pretending.
	Live bool `json:"live"`
}

// ManagedEnvKeys is the curated list the console offers first. It is a
// starting point, not a cage: any SCREAMING_SNAKE variable that is not on the
// deny list below can be typed in and saved too — the spec only tells the
// console how to label, mask and check the ones we know.
var ManagedEnvKeys = []ManagedEnv{
	{
		Key:    "CRYPTOREFILLS_PARTNER_KEY",
		Label:  "CryptoRefills partner key",
		Hint:   "The v6 partner API key (cr_pk_…). Orders are created on /v6/partner/orders with it — without it the shop earns no commission. Applied the moment you save it.",
		Secret: true,
		Live:   true,
	},
	{
		Key:    "CRYPTOREFILLS_PARTNER_KEY_BACKUP",
		Label:  "CryptoRefills backup key",
		Hint:   "A second cr_pk_… key. When the primary is rejected (revoked, 401/403) the shop switches to this one automatically instead of going down.",
		Secret: true,
		Live:   true,
	},
	{
		Key:   "CRYPTOREFILLS_PARTNER_ID",
		Label: "CryptoRefills partner id",
		Hint:  "The X-Cr-Application value from the partner account page. Takes effect on the next start.",
	},
	{
		Key:   "CRYPTOREFILLS_BASE_URL",
		Label: "CryptoRefills API base URL",
		Hint:  "The https origin of the supplier API. Takes effect on the next start.",
	},
	{
		Key:   "SITE_HOST",
		Label: "Shop hostname",
		Hint:  "The public hostname (e.g. shop.example.com) used in emails, wallet memos and /api/site. Takes effect on the next start.",
	},
	{
		Key:    "POOL_FEED_API_KEY",
		Label:  "Pool feed key",
		Hint:   "Shared secret for the staker-boost profit feed. Takes effect on the next start.",
		Secret: true,
	},
	{
		Key:   "DAILY_ORDER_LIMIT",
		Label: "Daily order ceiling",
		Hint:  "How many orders one account may open in 24h (0 disables). Takes effect on the next start.",
	},
	{
		Key:   "DAILY_SPEND_LIMIT_USD",
		Label: "Daily spend ceiling (USD)",
		Hint:  "Published daily purchase budget per account (0 disables). Takes effect on the next start.",
	},
	{
		Key:   "MONTHLY_SPEND_LIMIT_USD",
		Label: "Monthly spend ceiling (USD)",
		Hint:  "Published monthly purchase budget per account (0 disables). Takes effect on the next start.",
	},
}

// envDenyList is the set of variables the console refuses to touch. They are
// not "sensitive" — they are load-bearing for the process itself: changing
// one from inside the running app can move the database out from under the
// shop or lock the operator out of the very console they are using. Those
// belong to the host (Railway dashboard), where a bad value can be undone
// without needing the app to be reachable.
var envDenyList = map[string]string{
	"BADGER_DIR":           "it points at the database directory; move it on the host",
	"LISTEN_ADDR":          "it decides which port the API binds; change it on the host",
	"PORT":                 "it decides which port the API binds; change it on the host",
	"STATIC_DIR":           "it points at the built frontend; change it on the host",
	"JWT_SECRET":           "rotating it signs every customer out; rotate it on the host",
	"ADMIN_SESSION_SECRET": "rotating it signs the operator out of this console; rotate it on the host",
}

var envKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`)

// ManagedEnvSpec returns the curated spec for a key, if there is one.
func ManagedEnvSpec(key string) (ManagedEnv, bool) {
	for _, spec := range ManagedEnvKeys {
		if spec.Key == key {
			return spec, true
		}
	}
	return ManagedEnv{}, false
}

// ValidateEnvKey accepts any well-shaped variable name that is not on the
// deny list. Unknown names are allowed on purpose (see ManagedEnvKeys) — they
// are exported into the process and applied at boot — but they are never
// silently accepted: the caller tells the operator that the shop cannot check
// the meaning of a name it does not know.
func ValidateEnvKey(key string) error {
	key = strings.TrimSpace(key)
	if !envKeyRe.MatchString(key) {
		return fmt.Errorf("%q is not an environment variable name — use SCREAMING_SNAKE_CASE, e.g. CRYPTOREFILLS_PARTNER_KEY", key)
	}
	if why, denied := envDenyList[key]; denied {
		return fmt.Errorf("%s is set by the host, not here: %s", key, why)
	}
	return nil
}

// ValidateEnvValue checks one value before it is stored. A value that would
// take the checkout down (a partner key that is not a partner key, a
// supplier URL that is not https) is rejected here, at save time, with the
// reason — the same rule config.Validate applies at boot.
func ValidateEnvValue(key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s needs a value — to stop overriding it, remove the key instead", key)
	}
	if strings.ContainsAny(value, "\r\n\t") {
		return fmt.Errorf("%s must be a single line without tabs", key)
	}
	switch key {
	case "CRYPTOREFILLS_PARTNER_KEY":
		if !strings.HasPrefix(value, "cr_pk_") {
			return fmt.Errorf("CRYPTOREFILLS_PARTNER_KEY must be a v6 partner key (it starts with cr_pk_); got %d characters with an unexpected prefix", len(value))
		}
	case "CRYPTOREFILLS_PARTNER_KEY_BACKUP":
		if !strings.HasPrefix(value, "cr_pk_") {
			return fmt.Errorf("CRYPTOREFILLS_PARTNER_KEY_BACKUP must be a v6 partner key (it starts with cr_pk_); got %d characters with an unexpected prefix", len(value))
		}
	case "CRYPTOREFILLS_BASE_URL":
		if err := httpsURL(key, value); err != nil {
			return err
		}
	case "SITE_HOST":
		host := normalizeSiteHost(value)
		if len(host) < 3 || len(host) > 253 || strings.ContainsAny(host, " \t\n:/?#") {
			return fmt.Errorf("SITE_HOST must be a hostname such as shop.example.com")
		}
	case "DAILY_ORDER_LIMIT":
		if n, err := strconv.Atoi(value); err != nil || n < 0 || n > 1_000_000 {
			return fmt.Errorf("DAILY_ORDER_LIMIT must be a whole number from 0 (disabled) through 1000000")
		}
	case "DAILY_SPEND_LIMIT_USD", "MONTHLY_SPEND_LIMIT_USD":
		amount, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount > 9e12 || (amount > 0 && amount < 0.000001) {
			return fmt.Errorf("%s must be 0 (disabled) or a USD amount from 0.000001 through 9000000000000", key)
		}
	}
	return nil
}

// ApplyEnvOverrides exports saved console values into the process environment
// and returns a copy of cfg with every field it knows updated.
//
// It is deliberately NOT fatal on a bad row: a stored value must never be
// able to keep the server from starting, because the console is the only way
// back in. Anything rejected is reported in warnings and left out — the host
// value (or the compiled default) keeps standing.
func ApplyEnvOverrides(cfg Config, overrides map[string]string) (Config, []string) {
	warnings := make([]string, 0, len(overrides))
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	// Sorted so the boot log reads in a stable order and a warning can be
	// matched to the same line twice in a row.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, key := range keys {
		value := strings.TrimSpace(overrides[key])
		if err := ValidateEnvKey(key); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		if err := ValidateEnvValue(key, value); err != nil {
			warnings = append(warnings, fmt.Sprintf("%v", err))
			continue
		}
		// Export first: anything that reads the environment later in this
		// process (a reload, a library that reads env directly) sees the
		// operator's value, not the host's.
		if err := os.Setenv(key, value); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		switch key {
		case "CRYPTOREFILLS_PARTNER_KEY":
			cfg.CRPartnerKey = value
		case "CRYPTOREFILLS_PARTNER_KEY_BACKUP":
			cfg.CRPartnerKeyBackup = value
		case "CRYPTOREFILLS_PARTNER_ID":
			cfg.CRPartnerID = value
		case "CRYPTOREFILLS_BASE_URL":
			cfg.CRBaseURL = strings.TrimRight(value, "/")
		case "SITE_HOST":
			cfg.SiteHost = normalizeSiteHost(value)
		case "POOL_FEED_API_KEY":
			cfg.PoolFeedAPIKey = value
		case "DAILY_ORDER_LIMIT":
			if n, err := strconv.Atoi(value); err == nil {
				cfg.DailyOrderLimit = n
			}
		case "DAILY_SPEND_LIMIT_USD":
			if amount, err := strconv.ParseFloat(value, 64); err == nil {
				cfg.DailySpendLimitUSD = amount
			}
		case "MONTHLY_SPEND_LIMIT_USD":
			if amount, err := strconv.ParseFloat(value, 64); err == nil {
				cfg.MonthlySpendLimitUSD = amount
			}
		default:
			if _, known := ManagedEnvSpec(key); !known {
				warnings = append(warnings, fmt.Sprintf("%s: applied to the process, but the shop does not know this variable — check that the name is right", key))
			}
		}
	}
	return cfg, warnings
}

// SnapshotEnv copies the process environment as it stands right now. The
// server takes one at boot, BEFORE console overrides are exported on top of
// it, so the host's own value stays answerable afterwards (see
// handlers.HostEnv). The environment is small and this runs once.
func SnapshotEnv() map[string]string {
	out := make(map[string]string, 64)
	for _, entry := range os.Environ() {
		if i := strings.IndexByte(entry, '='); i > 0 {
			out[entry[:i]] = entry[i+1:]
		}
	}
	return out
}

// MaskValue renders a value for the console. Secrets are never shown whole:
// enough to recognise which key is loaded, never enough to copy it out of the
// screen. Anything short enough that masking would leave the whole thing
// (or be pointless) is shown as dots.
func MaskValue(value string, secret bool) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !secret {
		if len(value) > 160 {
			return value[:160] + "…"
		}
		return value
	}
	if len(value) <= 12 {
		return strings.Repeat("•", 8)
	}
	head := value[:6]
	tail := value[len(value)-2:]
	return head + "…" + tail + fmt.Sprintf(" (%d characters)", len(value))
}
