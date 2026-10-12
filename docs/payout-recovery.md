# Cashback and quiz payment recovery

Both queues use one configured sender and the same persisted Badger volume, with synchronous writes enabled by default. Prize rows have their own deterministic competition/user keys and never debit the purchase cashback or staking ledger. They are displayed in the owner's cashback history with their memo and transaction link.

- No balance: the obligation stays queued and is checked again on worker ticks. It is not signed until the wallet can cover amount and fee.
- Before broadcast: exact signed bytes, canonical content hash and validity height are committed durably. Same-value obligations include unique memo references so they cannot accidentally share a transaction hash.
- Lost RPC reply or crash after broadcast: first look up the persisted canonical hash; retain and retry the same bytes while valid. Any provider seeing the payment blocks replacement. Only all configured RPCs agreeing on successful finalized inclusion can mark it paid.
- Long outage / expired unpaid signature: obtain the live Albatross validity policy and a matching finalized macro block beyond the entire validity window from every configured RPC. After that checkpoint, every RPC must report the canonical transaction absent, or agree on the same finalized failed execution (which did not credit the recipient). Only then atomically retire the exact old bytes, preserving the expiry proof and audit history, and queue a fresh attempt. Timeouts, malformed responses, missing archival history or disagreement leave the obligation pending. Old in-flight callbacks cannot requeue a paid row or attach retired bytes.
- Persisted store reopening and competing workers: transactional state checks, signed-byte compare-and-set, deterministic prize keys, and a serialized tick per worker prevent creating parallel payable attempts. Queue selection favors the least recently attempted rows so a large unfunded payment does not indefinitely hide other obligations.

This relies on the configured RPC providers accurately reporting canonical archival history and on the persisted volume remaining intact. A single-provider configuration trusts that provider. Do not delete/reset the payment database, mount ephemeral storage, or run independently copied payment databases against the same real wallet. Backups do not replace the current ledger: restoring a stale snapshot requires on-chain reconciliation before resuming payments.

## Verification

`go test -race ./internal/cashback/worker ./internal/db ./internal/nimiq` covers signature durability, lost acknowledgements, failed receipt commits, failed paid commits, store reopening, insufficient balance, expired signatures, provider outages/disagreement, concurrent senders, memo uniqueness and owner-only prize history.

Real testnet verification is explicit opt-in:

```sh
NIMSHOP_RUN_TESTNET=1 go test -v ./internal/cashback/worker -run '^TestLiveNimiqTestnetPayoutRecovery$' -count=1 -timeout=7m
```

It creates ephemeral keys, uses only the official testnet faucet/RPC, sends two 1-NIM testnet payouts, suppresses one real broadcast acknowledgement through a local proxy, resumes the worker and checks finalized transaction hashes plus an exact 2-NIM recipient balance. No production seed or mainnet RPC is read. It is skipped by ordinary CI. A real-network test is successful only when this test actually passes; local fault tests are separate evidence.

## Server availability

The sender runs while the backend is awake. Railway free-plan services require serverless sleeping unless they are cron services. Converting the public API service to a cron job is not an appropriate fix. Sleeping may delay the deadline/funding check until the next wake; the persisted obligations survive. Exact unattended scheduling needs an always-running backend or an independently authorized wake scheduler.
