package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fasthttp/router"
	"github.com/valyala/fasthttp"

	"nimiqshop/internal/admin"
	"nimiqshop/internal/cashback"
	cbworker "nimiqshop/internal/cashback/worker"
	"nimiqshop/internal/chainstake"
	"nimiqshop/internal/clientip"
	"nimiqshop/internal/config"
	"nimiqshop/internal/cryptorefills"
	"nimiqshop/internal/db"
	"nimiqshop/internal/handlers"
	"nimiqshop/internal/httpx"
	"nimiqshop/internal/i18n"
	"nimiqshop/internal/mailtrap"
	"nimiqshop/internal/middleware"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/notification"
	"nimiqshop/internal/poolstake"
	"nimiqshop/internal/presence"
	"nimiqshop/internal/safe"
	"nimiqshop/internal/settlement"
	"nimiqshop/internal/stakeledger"
)

// wireStakeSources installs the two independent sources of "is this buyer
// staking in our pool?": the pool's own index — which owns the profit feed,
// the loyalty age and the pool's staker base — and a chain verifier that
// answers the raw delegation fact straight from the Nimiq chain. main()
// calls this at boot, and the integration tests call it too, so the tests
// exercise the wiring production runs: a fallback that only lives inside
// main() is a fallback no test can ever catch.
func wireStakeSources(h *handlers.Handlers, store *db.Store, cfg config.Config) {
	if cfg.PoolAPIURL == "" {
		log.Printf("poolstake: POOL_API_URL unset — staker cashback programme disabled")
		return
	}

	pool := poolstake.New(cfg.PoolAPIURL,
		time.Duration(cfg.PoolStakeTimeout)*time.Second,
		time.Duration(cfg.PoolStakeCacheTTL)*time.Second)
	h.Pool = pool
	// The chain verifier is the second, independent source for the raw
	// delegation fact. It matters because the pool's staker table is a
	// separate database that can lag or lose data (observed live: the
	// pool reported zero stakers while the chain showed delegators to
	// the pool's own validator) — and when it does, every real staker is
	// silently paid and shown the non-staker rate.
	chain := chainstake.New(nimiq.NewClient(cfg.NimiqRPCURL, cfg.NimiqRPCURL2), cfg.PoolValidatorAddress, 0, 0)
	h.Chain = chain
	store.SetStakerLookupDetailed(func(ctx context.Context, address string) (db.StakerStake, error) {
		st, err := pool.Stake(ctx, address)
		if err != nil {
			// A pool outage must never block delivery, must never be
			// upgraded into a free boost, and must never be mistaken
			// for a withdrawal: propagate the error so the clocks stay
			// untouched — UNLESS the chain can answer, in which case the
			// answer is authoritative and the outage is irrelevant.
			log.Printf("poolstake: lookup failed for %s: %v", poolstake.CanonicalAddress(address), err)
			if cs, cerr := chain.Verify(ctx, address); cerr == nil && cs.Staked {
				log.Printf("poolstake: chain confirms a %.2f NIM delegation while the pool is unreachable", float64(cs.StakeLuna)/100000)
				return db.StakerStake{StakeLuna: cs.StakeLuna, Staked: true, BaseBps: stakerBaseBps(store)}, nil
			}
			return db.StakerStake{}, err
		}
		if !st.Staked {
			// The pool says "no staker". Before that verdict is acted
			// on, let the chain (public, seconds old) confirm or deny
			// it: the pool's index is a copy of the chain, not the
			// chain.
			if cs, cerr := chain.Verify(ctx, address); cerr == nil && cs.Staked {
				log.Printf("poolstake: chain confirms a %.2f NIM delegation the pool did not report; using the chain",
					float64(cs.StakeLuna)/100000)
				st = poolstake.Status{Address: st.Address, StakeLuna: cs.StakeLuna, Staked: true, CheckedAt: cs.CheckedAt}
			}
		}
		// Fulfillment is the other useful refresh point: update only this
		// buyer immediately before the atomic cashback calculation.
		now := time.Now().UTC()
		_ = store.ObserveStakeLedger(address, st.StakeLuna, now)
		if cfg.PoolFeedAPIKey != "" {
			if q, qerr := h.Oracle.NIMUSD(ctx); qerr == nil && q.MedianUSD > 0 {
				month := now.Format("2006-01")
				if l, ok, _ := store.ReadStakeLedger(address); ok && l.ProfitMonth != "" && l.ProfitMonth != month {
					if p, perr := pool.Profit(ctx, address, "last_month"); perr == nil {
						_, _ = store.ApplyProfitSnapshot(address, now.AddDate(0, -1, 0).Format("2006-01"), p.PoolFeeLuna, q.MedianUSD, p.LoyaltyDays, p.LoyaltyMultiplier, p.StakeLuna, now)
					}
				}
				if p, perr := pool.Profit(ctx, address, "this_month"); perr == nil {
					_, _ = store.ApplyProfitSnapshot(address, month, p.PoolFeeLuna, q.MedianUSD, p.LoyaltyDays, p.LoyaltyMultiplier, p.StakeLuna, now)
				}
			}
		}
		// BaseBps is the POOL's number: its staker base while staked
		// (any amount), 0 otherwise. The shop applies it verbatim.
		return db.StakerStake{StakeLuna: st.StakeLuna, Staked: st.Staked, BaseBps: st.BaseBps}, nil
	})
	pool.SetFeedKey(cfg.PoolFeedAPIKey)
	log.Printf("poolstake: staker cashback armed against %s (validator %q)", cfg.PoolAPIURL, cfg.PoolValidatorAddress)
}

// stakerBaseBps is the shop's staker base rate (admin panel, default 1%).
// Read here for the fulfillment-time decision so the promo can be changed
// without a deploy.
func stakerBaseBps(store *db.Store) int {
	s, err := store.GetAdminSettings(0)
	if err != nil {
		return admin.DefaultStakerCashbackBps
	}
	return s.EffectiveStakerCashbackBps()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := config.LoadDotEnv(".env"); err != nil {
		log.Fatalf("load .env: %v", err)
	}
	cfg := config.Load()

	// Wallet-seed normalization: the operator may paste a 24-word BIP39
	// recovery phrase (Nimiq Hub / Nimiq Safe style) directly into
	// CASHBACK_WALLET_SEED or NOTIFY_WALLET_SEED instead of a hex seed. It is
	// derived here — exactly like the Hub does: BIP39 → SLIP-10 ed25519 →
	// m/44'/242'/<account>' — so everything downstream (validation, worker,
	// signer, banners) only ever sees the canonical 32-byte hex seed.
	resolveWalletSecret := func(name, value string, account uint32) string {
		if strings.TrimSpace(value) == "" {
			return value
		}
		seedHex, derived, err := nimiq.ResolveWalletSecret(value, account)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		if derived {
			if addr, aerr := nimiq.AddressFromKeyHex(seedHex); aerr == nil {
				log.Printf("%s: wallet derived from BIP39 recovery phrase (path m/44'/242'/%d') → %s — verify this address matches your wallet", name, account, addr)
			}
		}
		return seedHex
	}
	// Config.Validate rejects values outside 0..100, but bound-check here too
	// so the int→uint32 conversion is provably safe.
	cashbackAccount := cfg.CashbackWalletAccount
	if cashbackAccount < 0 || cashbackAccount > 100 {
		log.Fatalf("cashback: CASHBACK_WALLET_ACCOUNT out of range (0..100): %d", cashbackAccount)
	}
	cfg.CashbackWalletSeed = resolveWalletSecret("cashback: CASHBACK_WALLET_SEED", cfg.CashbackWalletSeed, uint32(cashbackAccount))
	if cfg.NotifyWalletSeed != "" {
		cfg.NotifyWalletSeed = resolveWalletSecret("notify: NOTIFY_WALLET_SEED", cfg.NotifyWalletSeed, 0)
	}
	cashback.ShopName = cfg.SiteName()
	notification.ShopName = cfg.SiteName()
	// Cross-domain deployments (frontend and API on different hostnames,
	// SESSION_COOKIE_SAME_SITE=none) need every browser cookie — session,
	// admin, language — to be SameSite=None; Secure, or the browser never
	// sends them back on the cross-site fetches and logins silently fail.
	i18n.SetCrossSiteCookies(cfg.SessionCookieSameSite == "none")
	if err := cfg.Validate(); err != nil {
		log.Fatalf("unsafe configuration: %v", err)
	}

	// Runtime envelope. At eight-figure traffic the garbage collector, not
	// the handler code, is what steals tail latency: every response allocates
	// a JSON buffer, and the default GOGC=100 means the heap is collected
	// every time it doubles. Both knobs are opt-in through env so a small VPS
	// keeps the runtime defaults.
	if cfg.GCPercent != 0 {
		prev := debug.SetGCPercent(cfg.GCPercent)
		log.Printf("runtime: GOGC %d -> %d (fewer collections, more headroom)", prev, cfg.GCPercent)
	}
	if cfg.MemoryLimitMB > 0 {
		debug.SetMemoryLimit(int64(cfg.MemoryLimitMB) << 20)
		log.Printf("runtime: soft memory limit %d MB (a flood degrades into GC pressure, not an OOM kill)", cfg.MemoryLimitMB)
	}

	// BadgerDB is embedded: this opens a directory on disk rather than
	// dialing a server, and there is no migration step because the store
	// is schemaless (the keyspace is documented in internal/db/keys.go).
	store, err := db.New(cfg.BadgerDir, db.Options{
		SyncWrites:       cfg.BadgerSyncWrites,
		ValueThresholdKB: cfg.BadgerValueThresholdKB,
	})
	if err != nil {
		log.Fatalf("db init: %v", err)
	}
	// Wire cashback runtime enrichment: burn wallet address. Both payment
	// rails (Nimiq Pay and USDT Polygon) pay the same cashback rate.
	store.SetCashbackEnrichment(cfg.BurnNimAddress)
	log.Printf("usdt: Polygon rail enabled — same cashback rate as Nimiq Pay")
	defer func() {
		// Closing Badger flushes pending writes; skipping it can leave
		// recent commits to be recovered from the WAL on next boot.
		if cerr := store.Close(); cerr != nil {
			log.Printf("badger close: %v", cerr)
		}
	}()

	// The seed consists of a pre-generated Argon2id PHC value and a TOTP
	// secret, never a raw administrator password. The store's marker makes
	// this one-time even if the process restarts with the variables present.
	if cfg.HasAdminSeed() {
		if _, err := store.BootstrapAdmin(cfg.AdminUsername, cfg.AdminPasswordHash, cfg.AdminTOTPSecret); err != nil && !errors.Is(err, db.ErrConflict) {
			log.Fatalf("admin bootstrap: %v", err)
		} else if err == nil {
			log.Printf("initial admin %q bootstrapped", cfg.AdminUsername)
		}
	}

	cr := cryptorefills.NewClient(cfg.CRBaseURL, cfg.CRPartnerID, cfg.CRAppVersion, cfg.CRUserAgent, cryptorefills.QueueConfig{
		MaxQueue:            cfg.CRQueueMax,
		MaxQueuePerActor:    cfg.CRQueuePerActorMax,
		ActorRequestsPerMin: cfg.CRActorPerMinute,
		ActorBurst:          cfg.CRActorBurst,
	})
	defer cr.Close()

	h := handlers.New(store, cfg, cr)

	// Owner (2026-10-05): the order-DB wipe is NOT a button — it runs by
	// itself, exactly once per install, on the first boot after this change.
	// Keep-rule: the real (non-simulated) UniPin purchase; every other quote
	// leaves the store. The meta marker makes it one-shot across restarts,
	// so orders created AFTER this wipe are never touched.
	if raw, _ := store.LoadMeta("purge_all_except_unipin_v1"); raw == nil {
		if n, orders, err := store.PurgeQuotesAllExceptUnipin(); err != nil {
			log.Printf("startup purge: %v", err)
		} else {
			log.Printf("startup purge: deleted %d quotes (%d supplier rows), UniPin kept", n, orders)
		}
		if err := store.SaveMeta("purge_all_except_unipin_v1", []byte(time.Now().UTC().Format(time.RFC3339)), 0); err != nil {
			log.Printf("startup purge: marker not saved: %v", err)
		}
	}

	// Owner (2026-10-05), pass 2: the wipe deleted the fake ORDERS but their
	// cashback ledger rows stayed behind — orphans that kept inflating the
	// public totals and the leaderboard with fake "pending" NIM. One-shot,
	// its own marker, same boot-time discipline.
	if raw, _ := store.LoadMeta("purge_orphan_cashbacks_v2"); raw == nil {
		if n, err := store.PurgeOrphanCashbacks(); err != nil {
			log.Printf("startup purge v2: %v", err)
		} else {
			log.Printf("startup purge v2: deleted %d orphan cashback rows", n)
		}
		if err := store.SaveMeta("purge_orphan_cashbacks_v2", []byte(time.Now().UTC().Format(time.RFC3339)), 0); err != nil {
			log.Printf("startup purge v2: marker not saved: %v", err)
		}
	}
	h.Presence = presence.New()
	// A shared (CDN) cache is keyed on the URL alone, so it may only store a
	// response whose Access-Control-Allow-Origin is DETERMINISTIC. With zero
	// configured origins the deployment is same-origin and no ACAO is ever
	// emitted; with exactly one, the same literal is emitted every time. With
	// two or more (apex + www) the header echoes the request, and caching it
	// would hand one origin's CORS grant to another — so the CDN layer
	// silently downgrades to private and only browser caching applies.
	h.CDNCacheSafe = cfg.CDNCachePublic && len(cfg.AllowedOrigins) <= 1
	if cfg.CDNCachePublic && !h.CDNCacheSafe {
		log.Printf("edge: CDN caching of public API responses disabled — %d ALLOWED_ORIGINS entries make the CORS header non-deterministic (browser caching still applies)", len(cfg.AllowedOrigins))
	}

	// Mailtrap is the ONE mail transport (the SMTP client and the SMS sender
	// were removed): fulfillment gift notes, the admin retry and the admin
	// test-email surface all send through this client. Wired from env;
	// unconfigured keeps it nil-safe (callers return a clear 503 instead of
	// attempting a send).
	if mtc, mtErr := mailtrap.New(mailtrap.ConfigFromEnv()); mtErr != nil {
		log.Printf("mailtrap: not enabled: %v", mtErr)
	} else {
		h.Mail = mtc
		if mtc.Enabled() {
			log.Printf("mailtrap: email transport enabled (sandbox=%v from=%q)", mtc.Sandbox(), mtc.Config().FromEmail)
		}
	}

	// Pool-staker cashback: buyers who delegate NIM to the operator's own
	// validator earn the admin-configured ladder instead of the base rate.
	// The client is optional — without POOL_API_URL nothing is wired and
	// every buyer keeps the base rate.
	//
	// The lookup is handed to the store (not called by the store) because it
	// must run OUTSIDE the fulfillment transaction: it is a network call, and
	// Badger replays a conflicting write closure.
	wireStakeSources(h, store, cfg)
	// Single-ledger staker cashback (Tek Defter): the parameter resolver
	// reads the admin settings live, so a console edit applies to the next
	// fulfillment/accrual without a restart.
	store.SetStakeLedgerParams(func() stakeledger.Params {
		settings, err := store.GetAdminSettings(0)
		if err != nil {
			return stakeledger.Defaults
		}
		return settings.EffectiveStakeCashback()
	})
	if cfg.PoolAPIURL != "" && cfg.PoolFeedAPIKey != "" {
		log.Printf("cashback profit: on-demand refresh armed (profile/refresh/fulfillment only)")
	} else if cfg.PoolAPIURL != "" {
		log.Printf("cashback profit: POOL_FEED_API_KEY unset — staker boost will not accrue (base rate unaffected)")
	}
	// 60s background NIM/BTC rate refresher: /api/market/nim-rate always
	// serves the warm snapshot instantly — user requests never wait on the
	// oracle (see internal/handlers/market_rates.go).
	h.StartRatesRefresher(ctx)

	// FX refresher: the USD rate table behind the admin's price cap and the
	// pre-quote display estimate follows the live market automatically (no
	// more hand-edited rates). Stale beats wrong: a failed fetch keeps the
	// previous snapshot, and the embedded baseline is the last resort. See
	// internal/handlers/fx_refresh.go.
	h.StartFXRefresher(ctx)

	// Post-fulfillment stake re-ask: a buyer who was not verifiably staked
	// at delivery time (wallet stake, pool index lag, pool blip) is
	// re-queried from the shop's own pool every minute for an hour, and
	// their still-unpaid cashback is upgraded the moment the pool says
	// "staked". No buyer claims, no trust — the pool is the single source.
	if cfg.PoolAPIURL != "" {
		h.StartStakeRechecker(ctx)
	}
	// Non-custodial: no wallet, treasury or refund signer on this server.
	// The only background task is the fulfillment tracker: it polls
	// non-terminal supplier orders (webhooks are an optional acceleration),
	// sweeps locally-unpaid quotes, and flags stuck order-creation intents.
	tracker := &settlement.OrderTracker{
		Store: store, CR: cr,
		Interval:   time.Duration(cfg.CRPollSecs) * time.Second,
		StaleAfter: time.Duration(cfg.CRStaleSecs) * time.Second,
	}
	tracker.Run(ctx)

	netID := byte(nimiq.NetworkMainnet)
	if cfg.CashbackNetwork == "testnet" {
		netID = byte(nimiq.NetworkTestnet)
	}
	cbWorker := &cbworker.Worker{
		Store: store, RPC: nimiq.NewClient(cfg.NimiqRPCURL, cfg.NimiqRPCURL2),
		SeedHex: cfg.CashbackWalletSeed, NetworkID: netID,
		FeeLuna: int64(cfg.CashbackFeeLuna), Enabled: cfg.CashbackEnabled,
	}
	cbWorker.Run(ctx)

	// 1-Luna WALLET MEMO channel. Until now this package existed but was
	// never constructed — settlement.NotifyFn stayed nil, so the "your
	// order is ready" memo the code was written for never fired. Wire it,
	// gated by NOTIFY_WALLET_ENABLED and the send policy in
	// internal/notification/policy.go (opt-out, cooldown, monthly budget).
	notifyNetID := byte(nimiq.NetworkMainnet)
	if cfg.NotifyWalletNetwork == "testnet" {
		notifyNetID = byte(nimiq.NetworkTestnet)
	}
	walletNotifier := notification.New(
		nimiq.NewClient(cfg.NimiqRPCURL, cfg.NimiqRPCURL2), store,
		cfg.NotifyWalletSeed, notifyNetID, int64(cfg.NotifyWalletFeeLuna), cfg.NotifyWalletEnabled,
	)
	h.WalletNotifier = walletNotifier
	if walletNotifier.Enabled() {
		log.Printf("notify: wallet memo channel enabled (network=%s fee=%d Luna)", cfg.NotifyWalletNetwork, cfg.NotifyWalletFeeLuna)
	}
	// The order-ready memo: one per fulfilled quote, keyed on the quote id
	// so a tracker re-run or crash-restart can never send it twice.
	settlement.SetNotifyFn(func(q db.Quote) {
		if q.UserID == "" {
			return
		}
		ctxN, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Admin test-center orders go through the same policy pipeline but
		// SIMULATED: no transaction is ever signed or broadcast.
		var err error
		if q.TestMode {
			_, err = walletNotifier.NotifyReasonSimulated(ctxN, notification.ReasonOrderFulfilled, "quote:"+q.ID, q.UserID, "")
		} else {
			_, err = walletNotifier.NotifyReason(ctxN, notification.ReasonOrderFulfilled, "quote:"+q.ID, q.UserID, "")
		}
		if err != nil {
			log.Printf("notify: order-ready memo for quote %s failed: %v", q.ID, err)
		}
	})

	// Gift notification: the recipient-facing email, sent through Mailtrap
	// (the only mail transport — no SMTP client, no SMS sender exists in
	// this build). Fires once per fulfilled gift order; the GiftNotifiedAt
	// marker makes it at-most-once across crashes and tracker re-runs.
	settlement.SetGiftNotifyFn(func(q db.Quote) {
		// Idempotency: skip when the marker is already set. This is the
		// gate that turns the fulfilled transition into at-most-once
		// delivery.
		if !q.GiftNotifiedAt.IsZero() {
			return
		}
		// Owner (2026-10-05): plain purchases mail the buyer too — the note
		// builder flips itself into order-confirmation wording (Self).
		if strings.TrimSpace(q.CustomerEmail) == "" {
			return // nobody to mail
		}
		if h.Mail == nil || !h.Mail.Enabled() {
			log.Printf("mailtrap: gift note for quote %s skipped (transport not configured)", q.ID)
			return
		}
		// The same builder the admin retry uses: both senders render the
		// note from ONE place (handlers.BuildGiftNoteFromQuote), and the
		// note itself seals an anonymous buyer's identity.
		note := h.BuildGiftNoteFromQuote(q, q.Lang)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		ids, err := h.Mail.SendGiftNote(ctx, note)
		if err != nil {
			// Not marked: a failure leaves the door open for a manual
			// retry from the admin console.
			log.Printf("mailtrap: gift note for quote %s failed: %v", q.ID, err)
			return
		}
		if err := store.MarkGiftNotified(q.ID); err != nil {
			log.Printf("mailtrap: gift note for quote %s sent (ids=%s) but failed to mark: %v", q.ID, strings.Join(ids, ","), err)
			return
		}
		log.Printf("mailtrap: gift note for quote %s sent (ids=%s anonymous=%v)", q.ID, strings.Join(ids, ","), q.Anonymous)
	})

	// Serve the storefront from disk snapshots BEFORE the first request
	// arrives: even if the supplier is down or 429-throttled, a fresh
	// process boots with a full catalog.
	h.PreloadCatalogSnapshots()

	// Supplier catalogue refresh is request-driven. The public API requires
	// END_USER_IP and END_USER_AGENT on every call; a synthetic cache-warming
	// actor has neither. Existing visual snapshots can still seed the UI.

	r := buildRouter(h, cfg)

	// Top-level panic logger. fasthttp already recovers handler panics
	// per-connection, but it swallows the stack trace silently. Wrapping the
	// whole router turns every handler panic into a loud, stack-bearing log
	// line and a clean 500, so an incident is diagnosable without an outage.
	// Request pipeline, outermost first. The ORDER MATTERS and is the whole
	// point of the layering:
	//
	//	1. panic recovery  — a handler bug is a 500 and a stack trace, never
	//	                       a dead worker or a silent hang.
	//	2. admission         — a flood is shed here, before ANY work: no
	//	                       header parsing beyond what fasthttp did, no
	//	                       client-IP resolution, no database, no supplier.
	//	3. security headers  — stamped on every response including the shed
	//	                       ones and the router's own 404/405 bodies.
	//	4. compression       — applied last, to the finished body.
	//	5. client-IP gate    — the single trust boundary for attribution; it
	//	                       fails closed and everything downstream reads
	//	                       its cached verdict.
	//	6. router (+ CORS + rate limits, wired in buildRouter)
	admission := httpx.NewAdmission(httpx.AdmissionConfig{
		MaxInFlight:       cfg.MaxInFlight,
		RetryAfterSeconds: 1,
	})
	compressor := httpx.NewCompressor(httpx.CompressConfig{
		Enabled:  cfg.ResponseCompression,
		Level:    cfg.CompressLevel,
		MinBytes: cfg.CompressMinBytes,
	})
	inner := clientip.Gate(cfg.TrustProxy, cfg.ClientIPPolicy(), r.Handler)
	inner = compressor.Wrap(inner)
	inner = httpx.Harden(httpx.HeaderConfig{
		SiteHost:          cfg.SiteName(),
		HSTS:              cfg.HSTS,
		HSTSMaxAgeSeconds: 63072000,
		HSTSPreload:       cfg.HSTSPreload,
		FrameDeny:         true,
		ServerHeader:      cfg.ServerHeader,
	}, inner)
	inner = admission.Limit(inner)
	// A reused connection may not live forever: without a cap a single client
	// can pin a socket (and its read+write buffers) indefinitely, which is the
	// cheapest possible memory-exhaustion attack. fasthttp's own
	// MaxKeepaliveDuration is documented as a no-op nowadays, so the cap is
	// enforced here: once a connection is older than the budget the response
	// carries "Connection: close" and the client reconnects.
	maxKeepalive := keepaliveDuration(cfg)
	server := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			func() {
				defer func() {
					if p := recover(); p != nil {
						log.Printf("SAFE http %s %s: recovered panic: %v\n%s",
							ctx.Method(), ctx.Path(), p, safe.StackTrace())
						ctx.Error("internal error", fasthttp.StatusInternalServerError)
					}
				}()
				inner(ctx)
			}()
			if maxKeepalive > 0 && time.Since(ctx.ConnTime()) > maxKeepalive {
				ctx.SetConnectionClose()
			}
		},
		ReadTimeout:        15 * time.Second,
		WriteTimeout:       15 * time.Second, // a stuck writer can no longer pin a goroutine forever
		IdleTimeout:        idleTimeout(cfg),
		MaxRequestBodySize: cfg.MaxRequestBodyBytes,
		DisableKeepalive:   false,
		// Ceiling on simultaneously HELD CONNECTIONS, not on in-flight work —
		// see concurrencyCeiling. Sized from RAM; the shedder above is what
		// reacts to a flood.
		Concurrency:           concurrencyCeiling(cfg, admission),
		MaxIdleWorkerDuration: 60 * time.Second,
		// TCP keepalive: proxies (Cloudflare/nginx) and mobile browsers hold
		// connections open; without this fasthttp closes them and every
		// request repays the TCP handshake.
		TCPKeepalive:       true,
		TCPKeepalivePeriod: 30 * time.Second,
		// Deliberately left generous. With ReduceMemoryUsage on (below) these
		// buffers are pooled and returned to fasthttp's sync.Pool the moment a
		// request is drained, so an idle socket holds none of them and their
		// size is multiplied by in-flight requests rather than by users.
		// Measured at 20 000 idle connections, 8192/8192 costs 10.45 KB per
		// connection against 10.24 KB for 4096/2048 — a 2% difference that is
		// not worth trading 4x of request-header headroom for, because running
		// out of read buffer is a 431 that closes the socket. See config.go.
		ReadBufferSize:  cfg.HTTPReadBufferBytes,
		WriteBufferSize: cfg.HTTPWriteBufferBytes,
		// Idle sockets stop retaining the body buffers of whatever they last
		// served. This used to be hard-coded false on the reasoning that a box
		// with plenty of RAM should spend RAM to save CPU. That reasoning was
		// right when the binding constraint was request rate and wrong once the
		// constraint became connection count: at ten million mostly-idle
		// keep-alive sockets the RAM term is the one that scales with users,
		// while the CPU term lands only on the active minority. fasthttp
		// documents this flag for precisely that case. Configurable rather than
		// hard-coded so the trade can be re-decided per deployment without a
		// code change. See config.go for the full argument and the measurement.
		ReduceMemoryUsage: cfg.HTTPReduceMemoryUsage,
	}
	// Exported for the health endpoint so an operator can watch the shedder
	// approaching its cliff before it trips.
	h.Admission = admission
	log.Printf("edge: admission ceiling=%d compress=%v(level=%d,min=%dB) hsts=%v tunnel-origins=%v cdn-cache=%v keepalive=%s idle=%s",
		admission.Stats().Max, cfg.ResponseCompression, cfg.CompressLevel, cfg.CompressMinBytes,
		cfg.HSTS, cfg.AllowTunnelOrigins, cfg.CDNCachePublic, keepaliveDuration(cfg), idleTimeout(cfg))

	go func() {
		log.Printf("nimiqshop backend listening on %s", cfg.ListenAddr)
		if err := server.ListenAndServe(cfg.ListenAddr); err != nil {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	_ = server.Shutdown()
}

// writeHealthJSON encodes the health payload. It lives here rather than in
// handlers so the health route stays free of any middleware that could itself
// be the thing under inspection.
func writeHealthJSON(ctx *fasthttp.RequestCtx, v any) {
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	b, err := json.Marshal(v)
	if err != nil {
		ctx.SetBodyString(`{"ok":false,"error":"health encoding failed"}`)
		return
	}
	ctx.SetBody(b)
}

// concurrencyCeiling is fasthttp's HARD in-flight cap. It sits a third above
// the admission ceiling so the shedder (which answers with a useful 503 and a
// Retry-After) always trips first; if it is ever bypassed, fasthttp still
// refuses to grow without bound.
// concurrencyCeiling returns the value handed to fasthttp's Server.Concurrency.
//
// It used to be derived from the admission shedder (soft + soft/3 + 1024), on
// the theory that a finite value stops a flood from becoming an unbounded
// goroutine explosion. That derivation was wrong about what the setting does.
// Server.Concurrency is not a limit on in-flight requests: fasthttp counts
// *held connections* against it and answers 503 to the next one once the count
// is exceeded (v1.55 server.go:2033). An idle keep-alive socket occupies a slot
// while doing no work whatsoever, so tying this number to the shedder's
// in-flight budget meant the shop could not hold more than ~12 000 simultaneous
// customers — most of whom were idle between page views and costing nothing but
// memory.
//
// The two limits are genuinely different and are now set independently:
//
//   - in-flight work is bounded by the admission shedder, which is the thing
//     that should react to a flood, because it sheds by CPU cost rather than by
//     connection count;
//   - held connections are bounded here, sized from RAM rather than from
//     attack assumptions, because the cost of a connection is memory and the
//     question "how many can this box hold" has an arithmetic answer.
func concurrencyCeiling(cfg config.Config, a *httpx.Admission) int {
	if cfg.MaxConcurrentConns > 0 {
		return cfg.MaxConcurrentConns
	}
	// Only reachable if the field was left unset despite Validate(); keep the
	// shedder-derived figure as a fallback so a misconfigured binary still has
	// a finite bound instead of fasthttp's 256K default.
	soft := a.Stats().Max
	if soft <= 0 {
		soft = int64(httpx.DefaultMaxInFlight())
	}
	hard := soft + soft/3 + 1024
	if hard > int64(^uint(0)>>1) {
		hard = int64(^uint(0) >> 1)
	}
	return int(hard)
}

func keepaliveDuration(cfg config.Config) time.Duration {
	if cfg.MaxKeepaliveDurationSecs > 0 {
		return time.Duration(cfg.MaxKeepaliveDurationSecs) * time.Second
	}
	return 75 * time.Second
}

func idleTimeout(cfg config.Config) time.Duration {
	if cfg.IdleTimeoutSecs > 0 {
		return time.Duration(cfg.IdleTimeoutSecs) * time.Second
	}
	return 30 * time.Second
}

// tierLimiters/tierPool are set by buildRouter and read by the health
// endpoint. They are written once during start-up, before the listener opens,
// and only read afterwards — no synchronisation needed on the request path.
var (
	tierLimiters []*httpx.TierLimiter
	tierPool     *httpx.BucketPool
)

func buildRouter(h *handlers.Handlers, cfg config.Config) *router.Router {
	r := router.New()
	// CORS: supports separate frontend/backend domains.
	// Set ALLOWED_ORIGINS to your frontend origin(s), e.g. https://shop.nimiqbase.com,https://www.shop.nimiqbase.com
	// For same-origin (nginx proxy) you can leave it empty — same-origin requests need no CORS.
	// For dev with live-proxy, the proxy makes requests same-origin to the browser, so CORS is not needed.
	r.GlobalOPTIONS = func(ctx *fasthttp.RequestCtx) {
		applyCORS(ctx, cfg)
		if len(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) > 0 {
			ctx.SetStatusCode(fasthttp.StatusNoContent)
		} else {
			// If origin not allowed, still return 204 for OPTIONS to avoid browser hanging,
			// but without CORS headers browser will block.
			ctx.SetStatusCode(fasthttp.StatusNoContent)
		}
	}

	// Two layers of rate limiting, and they do different jobs:
	//
	//   - rateLimiter (global, per IP, generous) is the blunt instrument. It
	//     exists so no single client can saturate the process. Its defaults
	//     are deliberately high enough that a real shopper — even one on a
	//     flaky connection retrying a checkout — never sees it.
	//   - the httpx tier limiters are per-RESOURCE-COST. What is actually
	//     scarce here is not CPU, it is the shared CryptoRefills partner
	//     budget, the Nimiq RPC, outbound email and Argon2id time. A tier
	//     prices each of those separately so one scripted client cannot
	//     spend the whole shop's supplier allowance on dry-runs.
	//
	// Tiers are keyed (tier, client IP) through ONE shared sharded bucket
	// pool, so they cost one lock on a 1/256 shard per request.
	rateLimiter := middleware.NewRateLimiter(cfg.RateLimitPerMinute, cfg.RateLimitBurst, cfg.TrustProxy)
	pool := httpx.NewBucketPool()
	// sharedIPMultiple sizes the aggregate per-IP ceiling as a multiple of the
	// per-account budget. The question it answers is "how many distinct real
	// shoppers could plausibly share one source address at the same moment?"
	// — a carrier CGNAT pool, a campus wifi, a corporate egress. Ten is enough
	// that a genuinely shared network never brushes the ceiling while still
	// putting a hard stop on the alternative attack, which is one host
	// rotating through freshly generated Nimiq keypairs to collect a full
	// per-account budget each time.
	const sharedIPMultiple = 10
	ipPolicy := cfg.ClientIPPolicy()
	// applyOverrides lets RATE_TIER_OVERRIDES re-price a tier at boot. The
	// compiled defaults in internal/httpx/tiers.go stay the production
	// numbers; an operator who knows their supplier budget is larger, or a
	// harness that is deliberately driving more traffic than a human would,
	// says so explicitly instead of everyone inheriting a looser limit.
	applyOverrides := func(t httpx.Tier) httpx.Tier {
		if r, ok := cfg.TierRateOverrides[strings.ToLower(t.Name)]; ok {
			log.Printf("edge: tier %q re-priced by RATE_TIER_OVERRIDES: %d/min burst %d (default was %d/min burst %d)",
				t.Name, r.PerMinute, r.Burst, t.PerMinute, t.Burst)
			t.PerMinute, t.Burst = r.PerMinute, r.Burst
		}
		return t
	}
	tier := func(t httpx.Tier) *httpx.TierLimiter {
		return httpx.NewTierLimiter(pool, applyOverrides(t), cfg.TrustProxy, ipPolicy)
	}
	// authedTier budgets the SAME tier against the verified JWT subject
	// instead of the client IP. Every route mounted behind RequireAuth uses
	// this, and the reason is a real-traffic one rather than a theoretical
	// one: a large share of shoppers arrive through CGNAT, a corporate
	// egress, a campus wifi or a mobile carrier pool, so hundreds of
	// different people present ONE source address. An IP-keyed checkout
	// budget would be pooled across all of them, and the hundredth person to
	// press "pay" during a busy minute would be refused for somebody else's
	// shopping. Keying on the account gives each shopper their own budget —
	// which is both kinder to normal users AND tighter on an abuser, since
	// they can no longer dilute their own limit by arriving from a shared
	// network. Anonymous callers keep the IP key.
	authedTier := func(t httpx.Tier) *httpx.TierLimiter {
		t = applyOverrides(t)
		// Aggregate per-IP ceiling behind the per-account budget. A Nimiq
		// identity is free — Hub login is a signature over a fresh keypair —
		// so an account-keyed budget alone can be multiplied without limit by
		// rotating wallets. This second, much looser budget is what one
		// physical host cannot exceed no matter how many identities it
		// invents, while staying far above what a NAT'd crowd of real
		// shoppers would ever reach together.
		shared := httpx.Tier{
			Name:       t.Name + "-ip",
			PerMinute:  t.PerMinute * sharedIPMultiple,
			Burst:      t.Burst * sharedIPMultiple,
			RetryAfter: t.RetryAfter,
		}
		if r, ok := cfg.TierRateOverrides[strings.ToLower(shared.Name)]; ok {
			shared.PerMinute, shared.Burst = r.PerMinute, r.Burst
		}
		return httpx.NewSubjectTierLimiter(pool, t, cfg.TrustProxy, ipPolicy, middleware.UserID).
			WithSharedIPCeiling(shared)
	}
	tLogin := tier(httpx.TierLogin)
	tCheckout := tier(httpx.TierCheckout)
	tRefresh := tier(httpx.TierRefresh)
	tPool := tier(httpx.TierPool)
	tPromo := tier(httpx.TierPromo)
	tSupport := tier(httpx.TierSupport)
	tPresence := tier(httpx.TierPresence)
	tWrite := tier(httpx.TierWrite)
	tAdmin := tier(httpx.TierAdmin)
	// Account-keyed counterparts of the tiers that sit behind authentication.
	aCheckout := authedTier(httpx.TierCheckout)
	aRefresh := authedTier(httpx.TierRefresh)
	aPool := authedTier(httpx.TierPool)
	aSupport := authedTier(httpx.TierSupport)
	aPresence := authedTier(httpx.TierPresence)
	aWrite := authedTier(httpx.TierWrite)
	// The tiers are attached to the router so /api/health can report what
	// each one shed — the difference between "the site is slow" and "someone
	// is hammering checkout" is otherwise invisible. Both keyings are
	// reported: they are separate bucket namespaces over one pool.
	tierLimiters = append(tierLimiters[:0],
		tLogin, tCheckout, tRefresh, tPool, tPromo, tSupport, tPresence, tWrite, tAdmin,
		aCheckout, aRefresh, aPool, aSupport, aPresence, aWrite)
	tierPool = pool

	wrap := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return rateLimiter.Limit(func(ctx *fasthttp.RequestCtx) {
			applyCORS(ctx, cfg)
			next(ctx)
		})
	}
	// tiered adds a resource-cost budget INSIDE the global limiter, so a
	// shed request never reaches the handler at all.
	tiered := func(t *httpx.TierLimiter, next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return wrap(t.Limit(next))
	}
	// pinPrivate stamps "never cache this, anywhere" on the way out.
	//
	// This is not a nicety. Every authenticated route returns a body that is
	// a pure function of the Authorization header, and nothing in HTTP stops
	// a misconfigured CDN, a corporate proxy or a browser extension from
	// storing a URL-keyed response and replaying it to the next person who
	// asks for the same URL. /api/orders and /api/quotes/{id} are exactly
	// that shape: same path for every user, different body per bearer token.
	// Declaring it once here means a NEW authed route cannot forget it.
	pinPrivate := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			next(ctx)
			httpx.NoStore(ctx)
		}
	}
	// publicCached marks a genuinely user-independent GET as shareable, with
	// a stale-while-revalidate tail so a cold cache is a background job and
	// never a user-visible stall. It is applied ONLY to routes whose body
	// cannot vary by identity or client IP; httpx additionally refuses to
	// emit a shared-cache directive at all unless CDNCacheSafe is set (a
	// wildcard CORS answer would otherwise let one origin's
	// Access-Control-Allow-Origin be served to another).
	publicCached := func(maxAge, swr int, next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			next(ctx)
			// Only a clean 200 is cacheable. An error body must never be
			// pinned into a CDN and served to everyone for the next five
			// minutes because the origin had one bad second.
			if ctx.Response.StatusCode() == fasthttp.StatusOK {
				h.PublicCache(ctx, maxAge, swr)
			} else {
				httpx.NoStore(ctx)
			}
		}
	}
	authed := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return wrap(pinPrivate(middleware.RequireAuth(cfg.JWTSecret, authOpts(cfg), next)))
	}
	// authedTiered authenticates FIRST (cheap: one HMAC) and then applies the
	// cost budget to the identified caller. An unauthenticated flood is
	// already stopped by the global limiter, so ordering it this way means a
	// tier never wastes a bucket entry on a request that was going to 401.
	authedTiered := func(t *httpx.TierLimiter, next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return wrap(pinPrivate(middleware.RequireAuth(cfg.JWTSecret, authOpts(cfg), t.Limit(next))))
	}
	// This is a completely independent identity plane. It only accepts an
	// HttpOnly admin-session cookie; customer Bearer JWTs cannot satisfy it.
	adminAuthPublic := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		// Password/TOTP endpoints receive the tightest independent limiter in
		// the system: the operations console is one human being.
		return httpx.NewTierLimiter(pool, httpx.TierAdminLogin, cfg.TrustProxy, ipPolicy).
			Limit(func(ctx *fasthttp.RequestCtx) { applyCORS(ctx, cfg); next(ctx); httpx.NoStore(ctx) })
	}
	adminOnly := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return wrap(pinPrivate(tAdmin.Limit(h.RequireAdminSession(next))))
	}

	// Auth (Nimiq Hub wallet login: challenge -> Hub signMessage() -> verify)
	r.POST("/api/auth/challenge", tiered(tLogin, h.AuthChallenge))
	r.POST("/api/auth/hub-login", tiered(tLogin, h.HubLogin))
	// Session lifecycle for cookie-authenticated browsers. /api/auth/session is
	// how the frontend restores sign-in state on a page load: the JWT is
	// HttpOnly, so the page can no longer read it out of storage and has to ask.
	// GET is safe here — it reads nothing but the caller's own session, and a
	// Lax cookie is attached to cross-site GET navigations, so a state-changing
	// GET would be CSRF-able. This one is not state-changing.
	r.GET("/api/auth/session", wrap(pinPrivate(tRefresh.Limit(h.AuthSession))))
	// Logout has to be server-side now: the frontend cannot delete an HttpOnly
	// cookie, and a client-only "sign out" that leaves a valid seven-day
	// session cookie in the jar is not a logout at all.
	r.POST("/api/auth/logout", wrap(pinPrivate(tLogin.Limit(h.AuthLogout))))

	// Admin authentication. Bootstrap accepts only a deployment secret and a
	// pre-generated password hash; all subsequent routes use the distinct
	// cookie session and never the customer JWT middleware.
	r.POST("/api/admin/auth/bootstrap", adminAuthPublic(h.AdminBootstrap))
	r.POST("/api/admin/auth/login", adminAuthPublic(h.AdminLogin))
	r.POST("/api/admin/auth/logout", adminOnly(h.AdminLogout))
	r.GET("/api/admin/auth/me", adminOnly(h.AdminMe))
	r.GET("/api/admin/dashboard", adminOnly(h.AdminDashboard))
	r.GET("/api/admin/users", adminOnly(h.AdminListUsers))
	r.GET("/api/admin/users/{id}", adminOnly(h.AdminUserDetail))
	r.GET("/api/admin/orders", adminOnly(h.AdminListOrders))
	r.GET("/api/admin/orders/{id}", adminOnly(h.AdminGetOrderDetail))
	r.POST("/api/admin/orders/{id}/sync", adminOnly(h.AdminSyncOrder))
	r.POST("/api/admin/orders/{id}/refund", adminOnly(h.AdminRefundOrder))
	r.GET("/api/admin/quotes", adminOnly(h.AdminListQuotes))
	r.GET("/api/admin/transactions", adminOnly(h.AdminListTransactions))
	r.GET("/api/admin/manual-review", adminOnly(h.AdminManualReview))
	r.POST("/api/admin/quotes/{id}/resolve", adminOnly(h.AdminResolveQuote))
	r.POST("/api/admin/quotes/{id}/send-gift-notification", adminOnly(h.AdminSendGiftNotification))
	// Operator-direct notifications (no quote required): email — the one channel.
	r.POST("/api/admin/notification/send", adminOnly(h.AdminSendNotification))
	r.GET("/api/admin/notification/status", adminOnly(h.AdminNotificationStatus))
	r.POST("/api/admin/test-email", adminOnly(h.AdminSendTestEmail))
	// Operator sandbox: buy a real product on the simulated supplier, then
	// fake-pay it through the real state machine (see admin_test_center.go).
	r.POST("/api/admin/test-purchase", adminOnly(h.AdminTestPurchase))
	// The simulated-pay ENGINE stays for the e2e suite; every customer-facing
	// button that called it is gone (owner, 2026-10-05).
	r.POST("/api/quotes/{id}/test-pay", authedTiered(aCheckout, h.UserTestPay))
	r.GET("/api/admin/test-quote/{id}", adminOnly(h.AdminTestQuoteStatus))
	r.GET("/api/admin/oracle", adminOnly(h.AdminOracleHealth))
	r.POST("/api/admin/settings/margin", adminOnly(h.AdminUpdateMargin))
	// Operator-triggered FX refresh (the background refresher runs every 6h;
	// this is the "update the rates now" button). See internal/handlers/fx_refresh.go.
	r.POST("/api/admin/settings/fx-refresh", adminOnly(h.AdminRefreshFX))
	r.GET("/api/admin/settings/cashback", adminOnly(h.AdminGetCashback))
	r.POST("/api/admin/settings/cashback", adminOnly(h.AdminUpdateCashback))
	// Single-ledger staker programme: the parameter set is edited through
	// /settings/cashback; the table + per-wallet reset live here.
	r.GET("/api/admin/stake-ledger", adminOnly(h.AdminListStakeLedger))
	r.POST("/api/admin/stake-ledger/reset", adminOnly(h.AdminResetStakeLedger))
	r.GET("/api/admin/audit", adminOnly(h.AdminListAudit))

	// Admin catalog visibility rules (price cap, hidden families, banned
	// categories/kinds, country rules) + the UNFILTERED catalog views the
	// operator uses to see everything before hiding it.
	r.GET("/api/admin/catalog-rules", adminOnly(h.AdminGetCatalogRules))
	r.PUT("/api/admin/catalog-rules", adminOnly(h.AdminUpdateCatalogRules))
	r.GET("/api/admin/catalog/brands", adminOnly(h.AdminListBrands))
	r.GET("/api/admin/catalog/products/{productId}", adminOnly(h.AdminGetFamily))

	// Admin Support Tickets
	r.GET("/api/admin/support/tickets", adminOnly(h.AdminListSupportTickets))
	r.GET("/api/admin/support/tickets/{id}", adminOnly(h.AdminGetSupportTicket))
	r.POST("/api/admin/support/tickets/{id}/messages", adminOnly(h.AdminAddSupportMessage))
	r.POST("/api/admin/support/tickets/{id}/status", adminOnly(h.AdminUpdateSupportStatus))

	// Cryptorefills stablecoin checkout — the ONLY production purchase path.
	// The quote creates a supplier order and returns the one-time wallet
	// address + exact coin amount; the customer pays from their own wallet
	// and the tracker/webhook records the delivery. The server has no payment
	// wallet, treasury, balance or deposit route.
	r.POST("/api/quotes", authedTiered(aCheckout, h.CreateQuote))
	r.POST("/api/quotes/batch", authedTiered(aCheckout, h.CreateQuoteBatch))
	r.GET("/api/quotes", authed(h.ListUserQuotes))
	r.GET("/api/quotes/{id}", authed(h.GetUserQuote))
	r.POST("/api/quotes/{id}/rate", authedTiered(aWrite, h.RateQuote))

	// Catalog (public, no auth needed to browse)
	// 10 minutes shared, 1 hour served stale while revalidating: the brand
	// directory moves on a supplier timescale, and a stale directory is a
	// strictly better storefront than a spinner.
	r.GET("/api/catalog/brands", wrap(publicCached(600, 3600, h.ListBrands)))
	r.GET("/api/catalog/products", wrap(publicCached(300, 1800, h.GetFamily)))
	// Price is the one catalog field that must not go stale for long: it is
	// what the customer compares against what they will actually be charged.
	// 60s shared with a 5-minute stale tail keeps the hot path off the origin
	// without ever showing a price the quote engine would not honour.
	r.GET("/api/catalog/price", wrap(publicCached(60, 300, h.ProductPrice)))
	r.GET("/api/catalog/payment-vias", wrap(publicCached(3600, 21600, h.PaymentVias)))
	r.GET("/api/catalog/search", wrap(publicCached(300, 1800, h.SearchProducts)))
	r.GET("/api/catalog/products/{productId}", wrap(publicCached(300, 1800, h.GetProduct)))
	r.GET("/api/catalog/check-phone", tiered(tRefresh, h.CheckPhone))

	// Orders are read-only local views. New purchases always go through the
	// Supplier (CryptoRefills) Lightning quote above; status arrives via webhook.
	r.GET("/api/orders", authed(h.ListOrders))
	r.GET("/api/orders/{id}", authed(h.GetOrder))
	r.POST("/api/orders/{id}/refresh", authedTiered(aRefresh, h.RefreshOrder))
	r.GET("/api/orders/{id}/support", authed(h.GetOrderSupport))

	// Public activity feed + ratings (NO auth — fully transparent by design).
	// Discloses only what is already public on-chain (the paying wallet, the
	// amount) plus the buyer's voluntary star rating.
	r.GET("/api/activity", wrap(publicCached(30, 300, h.ListActivity)))
	r.GET("/api/ratings/summary", wrap(publicCached(300, 1800, h.RatingSummary)))
	// Public LIVE tracking: anyone can see an order's current stage by id, but
	// delivery codes stay owner-only. This is the anti-fraud transparency proof.
	// Public by design, but per-ORDER and meant to be live: caching it would
	// serve one buyer's delivery stage to whoever guesses the id next, and
	// freeze the anti-fraud transparency view.
	r.GET("/api/track/{id}", wrap(pinPrivate(h.TrackStatus)))
	// Client IP / country, resolved server-side. No third-party geo API call:
	// with Cloudflare in front it uses CF-Connecting-IP + CF-IPCountry; in
	// direct mode it returns the TCP peer. Same-origin, so the frontend never
	// talks to an external IP service.
	// Derived from the CLIENT IP, so it is per-caller by construction. A
	// shared cache here would hand one visitor another visitor's country.
	r.GET("/api/geo", wrap(pinPrivate(h.GeoInfo)))
	r.GET("/api/market/nim-rate", wrap(publicCached(30, 300, h.NIMRate)))
	r.GET("/api/market/fx", wrap(publicCached(300, 1800, h.FXRates)))
	r.GET("/api/site", wrap(publicCached(3600, 86400, h.PublicSite)))
	r.GET("/api/site-config", wrap(publicCached(300, 3600, h.SiteConfig)))
	r.GET("/api/cashback/rate", wrap(publicCached(300, 1800, h.PublicCashbackRate)))
	r.GET("/api/cashback/leaderboard", wrap(publicCached(60, 600, h.CashbackLeaderboard)))
	r.GET("/api/cashback/burn-balance", wrap(publicCached(60, 300, h.BurnWalletBalance)))
	// Echoes per-account redemption state for the code being looked up.
	r.GET("/api/cashback/code", tiered(tPromo, pinPrivate(h.PublicCashbackCode)))
	r.GET("/api/cashback/me", authed(h.CashbackMe))
	r.GET("/api/poolstake/me", authed(h.PoolStakeMe))
	r.POST("/api/poolstake/refresh", authedTiered(aPool, h.PoolStakeRefresh))
	// The buyer just broadcast a stake to our pool; the pool has NOT indexed
	// it yet. Record the pending note so the fulfillment path never pays a
	// real staker the base rate during the index-latency window.
	// Optional auth: a signed-in heartbeat counts the account, an anonymous
	// one counts the verified client IP. Never rejects — presence is a
	// cosmetic counter and must not be able to 401 a shopper.
	// OptionalAuth runs BEFORE the budget so a signed-in heartbeat is charged
	// to the account (one bucket however many tabs are open) and an anonymous
	// one to the verified client IP. Presence must never 401 a shopper, which
	// is why this is OptionalAuth and not RequireAuth.
	r.POST("/api/presence", wrap(pinPrivate(middleware.OptionalAuth(cfg.JWTSecret, authOpts(cfg), aPresence.Limit(h.PresenceHeartbeat)))))
	r.POST("/api/orders/{id}/rate", authedTiered(aWrite, h.RateOrder))
	r.GET("/api/account/limits", authed(h.GetAccountLimits))
	// Wallet-memo channel controls (opt-out).
	r.GET("/api/account/notifications", authed(h.GetNotificationPrefs))
	r.PUT("/api/account/notifications", authedTiered(aWrite, h.SetNotificationPrefs))

	// DEV-only free test products; not a customer payment rail.
	r.POST("/api/test/buy", authedTiered(aCheckout, h.TestBuy))
	r.POST("/api/quotes/{id}/payment-launch", authedTiered(aWrite, h.PaymentLaunch))
	// TEST MODE: the customer's simulated "pay" button — drives the quote
	// through the real state machine without any on-chain payment.
	r.POST("/api/quotes/{id}/refresh", authedTiered(aRefresh, h.RefreshQuote))

	// Customer Support Tickets
	r.POST("/api/support/tickets", authedTiered(aSupport, h.CreateSupportTicket))
	r.GET("/api/support/tickets", authed(h.ListUserSupportTickets))
	r.GET("/api/support/tickets/{id}", authed(h.GetSupportTicket))
	r.POST("/api/support/tickets/{id}/messages", authedTiered(aSupport, h.AddSupportMessage))
	// Buyer self-service close/reopen (resolved | open, ownership-checked).
	r.POST("/api/support/tickets/{id}/status", authedTiered(aSupport, h.UpdateSupportTicketStatusCustomer))

	// Webhooks (optional acceleration; the polling tracker is the guarantee):
	// the supplier retries non-2xx callbacks, but random internet traffic must
	// not occupy the supplier verification queue. The callback has its own
	// inbound limiter before it can enqueue any re-fetch calls.
	webhookRateLimiter := middleware.NewRateLimiter(120, 20, cfg.TrustProxy)
	r.POST("/api/webhooks/cryptorefills", webhookRateLimiter.Limit(h.CryptoRefillsWebhook))

	// /api/health is EXEMPT from admission control (see httpx.Admission) and
	// from the global rate limiter: an operator or a load balancer must be
	// able to ask "are you alive?" precisely when the answer is interesting.
	// It is also the place the whole edge posture is observable from, so an
	// incident can be diagnosed from one curl instead of a guess.
	r.GET("/api/health", func(ctx *fasthttp.RequestCtx) {
		stats := h.CR.QueueStats()
		throttled := "[]"
		if len(stats.Throttled) > 0 {
			// Values are built-in endpoint policy names, not user input.
			b, _ := json.Marshal(stats.Throttled)
			throttled = string(b)
		}
		ad := httpx.AdmissionStats{}
		if h.Admission != nil {
			ad = h.Admission.Stats()
		}
		tiers := make([]map[string]any, 0, len(tierLimiters))
		for _, t := range tierLimiters {
			tiers = append(tiers, map[string]any{"name": t.Name(), "rejected_total": t.Rejected()})
		}
		buckets := int64(0)
		if tierPool != nil {
			buckets = tierPool.Live()
		}
		ctx.Response.Header.Set("Cache-Control", "no-store")
		ctx.Response.Header.Set("CDN-Cache-Control", "no-store")
		writeHealthJSON(ctx, map[string]any{
			"ok": true,
			"cr_queue": map[string]any{
				"queued": stats.Queued, "actors": stats.Actors, "throttled": json.RawMessage(throttled),
			},
			"admission": map[string]any{
				"in_flight": ad.InFlight, "max_in_flight": ad.Max,
				"admitted_total": ad.Admitted, "rejected_total": ad.Rejected,
			},
			"rate_limit": map[string]any{
				"buckets":          buckets,
				"cors_blocked":     CORSBlocked(),
				"tiers":            tiers,
				"tunnel_origins":   cfg.AllowTunnelOrigins,
				"cdn_cache_public": h.CDNCacheSafe && cfg.CDNCachePublic,
			},
			"store": h.Store.HealthSnapshot(),
		})
	})

	if dir := strings.TrimSpace(cfg.StaticDir); dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			log.Printf("STATIC_DIR %q: %v — serving API only", dir, err)
		} else if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			log.Printf("STATIC_DIR %q is not a directory — serving API only", dir)
		} else {
			log.Printf("serving frontend from %s", abs)
			fs := &fasthttp.FS{
				Root:               abs,
				IndexNames:         []string{"index.html"},
				GenerateIndexPages: false,
				Compress:           true,
				AcceptByteRange:    true,
			}
			files := fs.NewRequestHandler()
			notFound := filepath.Join(abs, "404.html")
			r.NotFound = func(ctx *fasthttp.RequestCtx) {
				path := string(ctx.Path())
				if strings.HasPrefix(path, "/api/") {
					ctx.SetStatusCode(fasthttp.StatusNotFound)
					ctx.SetContentType("application/json")
					ctx.SetBodyString(`{"error":"not found"}`)
					return
				}
				files(ctx)
				if ctx.Response.StatusCode() == fasthttp.StatusNotFound {
					if _, err := os.Stat(notFound); err == nil {
						ctx.Response.Reset()
						ctx.SendFile(notFound)
						ctx.SetStatusCode(fasthttp.StatusNotFound)
					}
				}
			}
		}
	}

	return r
}

// authOpts resolves the auth middleware's options once from config.
func authOpts(cfg config.Config) middleware.AuthOptions {
	return middleware.AuthOptions{
		AllowedOrigins: cfg.AllowedOrigins,
		AllowTunnels:   cfg.AllowTunnelOrigins,
	}
}

func applyCORS(ctx *fasthttp.RequestCtx, cfg config.Config) {
	origin := string(ctx.Request.Header.Peek("Origin"))
	// No Origin header = same-origin request (nginx proxy or live-proxy), no CORS needed.
	if origin == "" {
		return
	}
	// Check allowed origins — supports exact match and wildcard subdomains if configured.
	for _, allowed := range cfg.AllowedOrigins {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		// Exact match
		if origin == allowed {
			ctx.Response.Header.Set("Access-Control-Allow-Origin", allowed)
			ctx.Response.Header.Set("Vary", "Origin")
			ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Admin-Bootstrap-Token, X-Requested-With, X-CSRF-Token")
			ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
			ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
			return
		}
		// Wildcard subdomain support: https://*.example.com
		if strings.HasPrefix(allowed, "https://*.") {
			domain := strings.TrimPrefix(allowed, "https://*.")
			if strings.HasPrefix(origin, "https://") {
				originHost := strings.TrimPrefix(origin, "https://")
				originHost = strings.Split(originHost, "/")[0]
				originHost = strings.Split(originHost, ":")[0]
				if originHost == domain || strings.HasSuffix(originHost, "."+domain) {
					ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
					ctx.Response.Header.Set("Vary", "Origin")
					ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
					ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Admin-Bootstrap-Token, X-Requested-With, X-CSRF-Token")
					ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
					ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
					return
				}
			}
		}
	}
	// Auto-allow Cloudflare Tunnel origins (trycloudflare.com) for dev/preview.
	//
	// GATED, and the gate matters: a quick tunnel hands out a RANDOM
	// subdomain of a domain anyone can obtain in two seconds
	// (`cloudflared tunnel --url ...`). Combined with
	// Access-Control-Allow-Credentials that meant an attacker's own tunnel
	// page could issue credentialed cross-origin requests to a signed-in
	// shopper's API and read the responses — orders, cashback balance,
	// support threads. It is now enabled only when ALLOW_TUNNEL_ORIGINS says
	// so, which config.Validate refuses on a non-sandbox deployment.
	if cfg.AllowTunnelOrigins && isTunnelOrigin(origin) {
		ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
		ctx.Response.Header.Set("Vary", "Origin")
		ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Admin-Bootstrap-Token, X-Requested-With, X-CSRF-Token")
		ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
		ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
		return
	}

	// Origin not in allowlist — don't set CORS headers (the browser blocks).
	//
	// The old code logged every rejection together with the FULL allowlist.
	// That was two problems: anyone who can send an Origin header could write
	// unbounded bytes to the service log (disk exhaustion, plus noise that
	// buries a real incident), and the allowlist is configuration with no
	// business being repeated per request. Blocked origins are now counted
	// and sampled, and the counter is exposed on /api/health.
	corsBlocked.Add(1)
	if n := corsBlocked.Load(); n <= 5 || n%1000 == 0 {
		log.Printf("CORS blocked origin: %s (total blocked: %d) path: %s", redactOrigin(origin), n, string(ctx.Path()))
	}
}

// corsBlocked counts rejected cross-origin requests. Sampling the log line
// keeps a hostile Origin header from becoming a disk-exhaustion vector.
var corsBlocked atomic.Int64

// CORSBlocked reports the running total for /api/health.
func CORSBlocked() int64 { return corsBlocked.Load() }

// isTunnelOrigin recognises a Cloudflare quick-tunnel hostname and nothing
// else. The old strings.Contains check would also have accepted
// "https://evil.com/x.trycloudflare.com" and
// "https://x.trycloudflare.com.evil.tld".
func isTunnelOrigin(origin string) bool {
	host, ok := strings.CutPrefix(origin, "https://")
	if !ok {
		return false
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if !strings.HasSuffix(host, ".trycloudflare.com") {
		return false
	}
	label := strings.TrimSuffix(host, ".trycloudflare.com")
	// A quick-tunnel label is one short DNS-safe segment; anything else means
	// the suffix was smuggled into a longer name.
	if label == "" || len(label) > 63 || strings.Contains(label, ".") {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// redactOrigin keeps a logged Origin from carrying control characters into
// the service log (log injection) and bounds its length.
func redactOrigin(origin string) string {
	o := origin
	if len(o) > 128 {
		o = o[:128] + "…"
	}
	o = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\t':
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, o)
	return strconv.Quote(o)
}
