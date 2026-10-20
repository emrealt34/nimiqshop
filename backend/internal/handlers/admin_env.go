package handlers

import (
	"os"
	"strings"
	"time"

	"nimiqshop/internal/config"
	"nimiqshop/internal/db"

	"github.com/valyala/fasthttp"
)

/*
	Environment variables, managed from the console.

	Owner (2026-10-19): "env anahtarlarını admin sayfasından ekleyip
	çıkarabilmeliyim. öyle her seferinde birşey ekliyeceksek kod değiştirelim
	gerek yok. mesela partner key gibi." — adding a key must not mean a code
	change and a deploy, and the partner key is the example that matters.

	How it works, and the three promises it keeps:

	  1. A value saved here lives in the shop's own database, so it survives
	     a redeploy onto a fresh filesystem (a .env file written next to the
	     binary does not — Railway throws it away).

	  2. It is exported into the process environment immediately AND re-applied
	     at boot, before anything reads it, so it behaves exactly like the same
	     variable set on the host.

	  3. The host always wins. Nothing here can overwrite what the host
	     provides, and REMOVING an override falls back to the host value
	     rather than to nothing — so an operator can experiment and undo it
	     without a dashboard.

	Secrets are write-only: the console shows a masked value so the operator
	can tell which key is loaded, never the key itself, and a saved value is
	never written to the audit log.
*/

// envEntry is one variable as the console shows it.
type envEntry struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Hint   string `json:"hint"`
	Secret bool   `json:"secret"`
	// Live: saving takes effect in the running process (the supplier keys are
	// swapped into the live client). False means "in force from the next
	// start on" — which the console says out loud instead of pretending.
	Live bool `json:"live"`
	// Known is false for a variable the shop has no spec for. It is still
	// stored and exported; the console warns that it cannot check the name.
	Known bool `json:"known"`
	// Source is "console" (an override is in force), "host" (the host value
	// is in force) or "unset".
	Source string `json:"source"`
	// Value is the masked in-force value (masked only for secrets).
	Value string `json:"value,omitempty"`
	// HostValue is the masked value removing the override falls back to.
	HostValue string `json:"host_value,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// effectiveEnvValue is the value in force for one key: the console override
// when there is one, otherwise what the host set at boot.
func (h *Handlers) effectiveEnvValue(key string, overrides map[string]string) string {
	if v := strings.TrimSpace(overrides[key]); v != "" {
		return v
	}
	if h.HostEnv != nil {
		return strings.TrimSpace(h.HostEnv[key])
	}
	return strings.TrimSpace(os.Getenv(key))
}

// envOverrideMap folds the stored rows into one key -> value map.
func (h *Handlers) envOverrideMap() (map[string]string, error) {
	rows, err := h.Store.ListEnvOverrides()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Key == "" {
			continue
		}
		out[row.Key] = row.Value
	}
	return out, nil
}

// applyLiveEnv pushes the in-force values into everything that can change
// without a restart: the process environment (so any later read, anywhere,
// sees the operator's value) and the live supplier client's partner keys.
func (h *Handlers) applyLiveEnv(overrides map[string]string) {
	for _, spec := range config.ManagedEnvKeys {
		value := h.effectiveEnvValue(spec.Key, overrides)
		if value == "" {
			// Nothing in force: make sure a stale exported value cannot keep
			// pretending to be one.
			_ = os.Unsetenv(spec.Key)
			continue
		}
		_ = os.Setenv(spec.Key, value)
	}
	if h.CR != nil {
		h.CR.SetPartnerKeys(
			h.effectiveEnvValue("CRYPTOREFILLS_PARTNER_KEY", overrides),
			h.effectiveEnvValue("CRYPTOREFILLS_PARTNER_KEY_BACKUP", overrides),
		)
	}
}

// AdminListEnv returns every variable the console can manage: the curated
// list first, then any override saved for a name the shop has no spec for.
func (h *Handlers) AdminListEnv(ctx *fasthttp.RequestCtx) {
	rows, err := h.Store.ListEnvOverrides()
	if err != nil {
		adminStoreError(ctx)
		return
	}
	stored := make(map[string]db.EnvOverride, len(rows))
	for _, row := range rows {
		stored[row.Key] = row
	}
	overrides := make(map[string]string, len(rows))
	for _, row := range rows {
		overrides[row.Key] = row.Value
	}

	keys := make([]string, 0, len(config.ManagedEnvKeys)+len(rows))
	for _, spec := range config.ManagedEnvKeys {
		keys = append(keys, spec.Key)
	}
	for key := range stored {
		if _, known := config.ManagedEnvSpec(key); !known {
			keys = append(keys, key)
		}
	}
	// Sorted: the console must not reshuffle itself between refreshes.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	entries := make([]envEntry, 0, len(keys))
	for _, key := range keys {
		spec, known := config.ManagedEnvSpec(key)
		row := stored[key]
		var hostValue string
		if h.HostEnv != nil {
			hostValue = strings.TrimSpace(h.HostEnv[key])
		} else {
			hostValue = strings.TrimSpace(os.Getenv(key))
		}
		effective := h.effectiveEnvValue(key, overrides)
		source := "unset"
		if strings.TrimSpace(row.Value) != "" {
			source = "console"
		} else if hostValue != "" {
			source = "host"
		}
		label, hint := key, "Saved from the console; the shop has no built-in description for this name."
		if known {
			label, hint = spec.Label, spec.Hint
		}
		entries = append(entries, envEntry{
			Key:       key,
			Label:     label,
			Hint:      hint,
			Secret:    spec.Secret,
			Live:      spec.Live,
			Known:     known,
			Source:    source,
			Value:     config.MaskValue(effective, spec.Secret),
			HostValue: config.MaskValue(hostValue, spec.Secret),
			UpdatedAt: row.UpdatedAt,
			UpdatedBy: row.UpdatedBy,
		})
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"keys": entries})
}

// AdminSetEnv saves (or replaces) one console override and exports it into
// the running process. The value is validated before it is stored: a partner
// key that is not a partner key is refused here, at save time, with the
// reason — the same rule config.Validate applies at boot.
func (h *Handlers) AdminSetEnv(ctx *fasthttp.RequestCtx) {
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := readJSON(ctx, &body); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "expected a JSON body with key and value")
		return
	}
	key := strings.TrimSpace(body.Key)
	if err := config.ValidateEnvKey(key); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := config.ValidateEnvValue(key, body.Value); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	identity := adminIdentity(ctx)
	row := db.EnvOverride{
		Key:       key,
		Value:     strings.TrimSpace(body.Value),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedBy: identity.User.ID,
	}
	if err := h.Store.SetEnvOverride(row); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	overrides, err := h.envOverrideMap()
	if err != nil {
		adminStoreError(ctx)
		return
	}
	h.applyLiveEnv(overrides)
	// The audit trail names the variable, never the secret.
	h.audit(identity.User.ID, "admin.env.set", ctx, key)
	spec, known := config.ManagedEnvSpec(key)
	hostValue := ""
	if h.HostEnv != nil {
		hostValue = h.HostEnv[key]
	}
	writeJSON(ctx, fasthttp.StatusOK, envEntry{
		Key: key, Label: spec.Label, Hint: spec.Hint, Secret: spec.Secret, Live: spec.Live, Known: known,
		Source:    "console",
		Value:     config.MaskValue(row.Value, spec.Secret),
		HostValue: config.MaskValue(hostValue, spec.Secret),
		UpdatedAt: row.UpdatedAt,
		UpdatedBy: row.UpdatedBy,
	})
}

// AdminDeleteEnv removes one console override. The variable does not
// disappear: it falls back to the value the host set (or to unset), and the
// live supplier client is told about it in the same breath. Deleting
// something that was never overridden is a no-op, not an error.
func (h *Handlers) AdminDeleteEnv(ctx *fasthttp.RequestCtx) {
	key, _ := ctx.UserValue("key").(string)
	key = strings.TrimSpace(key)
	if key == "" {
		writeError(ctx, fasthttp.StatusBadRequest, "a variable name is required")
		return
	}
	if err := config.ValidateEnvKey(key); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.Store.DeleteEnvOverride(key); err != nil {
		adminStoreError(ctx)
		return
	}
	overrides, err := h.envOverrideMap()
	if err != nil {
		adminStoreError(ctx)
		return
	}
	h.applyLiveEnv(overrides)
	identity := adminIdentity(ctx)
	h.audit(identity.User.ID, "admin.env.delete", ctx, key)

	hostValue := ""
	if h.HostEnv != nil {
		hostValue = h.HostEnv[key]
	}
	spec, known := config.ManagedEnvSpec(key)
	source := "unset"
	if hostValue != "" {
		source = "host"
	}
	writeJSON(ctx, fasthttp.StatusOK, envEntry{
		Key: key, Label: spec.Label, Hint: spec.Hint, Secret: spec.Secret, Live: spec.Live, Known: known,
		Source:    source,
		Value:     config.MaskValue(hostValue, spec.Secret),
		HostValue: config.MaskValue(hostValue, spec.Secret),
	})
}
