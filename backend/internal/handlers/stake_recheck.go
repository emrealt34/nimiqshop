package handlers

import (
	"context"
	"log"
	"time"

	"nimiqshop/internal/db"
)

// StartStakeRechecker launches the post-fulfillment stake re-ask loop: every
// 30 seconds the store re-queries the operator's own pool for every buyer who
// was not verifiably staked at delivery time, for up to an hour per order
// (see internal/db/stake_recheck.go — the why and the guarantees).
//
// The loop is deliberately dumb: all decisions (staked / keep asking /
// expire / outage-extension) live in Store.RunStakeRechecks, which is unit
// tested with a fake pool. This side only ticks and logs.
func (h *Handlers) StartStakeRechecker(stop context.Context) {
	if h.Cfg.PoolAPIURL == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(db.StakeRecheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop.Done():
				return
			case <-ticker.C:
				resolved, expired := h.Store.RunStakeRechecks(time.Now().UTC())
				if resolved > 0 {
					log.Printf("stake recheck: %d buyer(s) upgraded to the staker rate", resolved)
				}
				if expired > 0 {
					log.Printf("stake recheck: %d window(s) closed at the base rate", expired)
				}
			}
		}
	}()
	log.Printf("stake recheck: armed — unverified stakes are re-asked from the pool every 30s for %s", db.StakeRecheckWindow)
}
