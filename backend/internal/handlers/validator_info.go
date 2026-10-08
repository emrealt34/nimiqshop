package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/poolstake"
)

// validatorDirectoryURL is Nimiq's public validator registry (validators-api):
// the list the Nimiq Wallet reads for each validator's name and logo. It is
// public data and needs no account.
const validatorDirectoryURL = "https://validators-api-main.je-cf9.workers.dev/api/v1/validators"

const (
	directoryOKTTL   = time.Hour
	directoryFailTTL = 5 * time.Minute
	directoryMaxBody = 8 << 20
)

// directoryEntry is the part of one registry record the shop uses.
type directoryEntry struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Logo        string `json:"logo"`
	AccentColor string `json:"accentColor"`
}

// validatorDirectory caches the whole registry list (about 1 MB, a few dozen
// validators) and refreshes it at most once an hour. A failed refresh keeps the
// last good list and retries after a short wait, so an outage never blanks the
// names a buyer already sees.
type validatorDirectory struct {
	mu      sync.Mutex
	byAddr  map[string]directoryEntry
	expires time.Time
	client  *http.Client
	url     string
}

var validatorDir = &validatorDirectory{
	client: &http.Client{Timeout: 10 * time.Second},
	url:    validatorDirectoryURL,
}

// lookup returns the registry record for one validator address.
func (d *validatorDirectory) lookup(ctx context.Context, addr string) (directoryEntry, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var err error
	if time.Now().After(d.expires) {
		var fresh map[string]directoryEntry
		fresh, err = fetchDirectory(ctx, d.client, d.url)
		if err == nil {
			d.byAddr = fresh
			d.expires = time.Now().Add(directoryOKTTL)
		} else {
			d.expires = time.Now().Add(directoryFailTTL)
		}
	}
	e, ok := d.byAddr[poolstake.CanonicalAddress(addr)]
	return e, ok, err
}

func fetchDirectory(ctx context.Context, c *http.Client, url string) (map[string]directoryEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("validator directory answered %d", res.StatusCode)
	}
	var list []directoryEntry
	if err := json.NewDecoder(io.LimitReader(res.Body, directoryMaxBody)).Decode(&list); err != nil {
		return nil, err
	}
	out := make(map[string]directoryEntry, len(list))
	for _, e := range list {
		out[poolstake.CanonicalAddress(e.Address)] = e
	}
	return out, nil
}

// ValidatorInfo answers GET /api/poolstake/validator?address=NQ… with the public
// name, logo and accent colour of a validator, so the cashback page can show
// the validator the buyer currently stakes with. An address the directory does
// not list answers found=false, and the page then shows the address alone.
func (h *Handlers) ValidatorInfo(ctx *fasthttp.RequestCtx) {
	addr := string(ctx.QueryArgs().Peek("address"))
	if err := nimiq.ValidateAddress(addr); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid validator address")
		return
	}
	cctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	e, found, err := validatorDir.lookup(cctx, addr)
	resp := map[string]any{
		"address": poolstake.CanonicalAddress(addr),
		"found":   found,
	}
	if found {
		resp["name"] = e.Name
		resp["logo"] = e.Logo
		resp["accent_color"] = e.AccentColor
	} else if err != nil {
		resp["registry"] = "unavailable"
	}
	writeJSON(ctx, fasthttp.StatusOK, resp)
}
