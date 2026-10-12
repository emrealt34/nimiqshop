# Catalog and market refresh operations

The public `/status` page reads the report inside the **served snapshot**, not
the repository seed files. The footer links to this page. Each refreshed
snapshot contains the main GitHub run ID, catalog time, country failures,
full stock check counts, crypto provider observations and fiat provider
date. An Actions success without new served data cannot make the page fresh.

`catalog-refresh.yml` runs at `17 */6 * * *`: 00:17, 06:17, 12:17 and
18:17 UTC (03:17, 09:17, 15:17 and 21:17 in Turkey). Every scheduled slot
runs a full scan, even after a recent manual refresh. One shared pacing gate
limits stock reads to two per second across four workers; refusals slow it to
one per second and honour Retry-After. Every public family is revisited; warm
stock records cannot skip it. No e-money/KYC families are probed.

The stock budget is 180 minutes and the refresh job timeout is 200 minutes,
so normal scans can take 1–1.5 hours without the old 20-minute cut-off.
Provider outages can still prevent complete verification. The separate
completeness job fails when any stock check is pending/unresolved or a
country lookup failed. The useful partial snapshot and good rate updates
are still retained and deployed, with incomplete status publicly visible.
An empty country directory is read twice before publishing its empty list.

Runs share one concurrency group and never cancel an active scan. Catalog
and rates remain in one artifact and deployment; no data branches or PRs.
The backup guard uses six hours, with 270 minutes allowed for active work.
The status page allows an eight-hour freshness window (six-hour cadence plus
scan/deploy time), and independently requires a complete scan before showing
its success banner. Provider observation timestamps remain unchanged.

## Read-only Railway monitor

The `catalog-watchdog` Railway function runs `scripts/catalog-monitor.mjs` once every six hours with cron `47 3,9,15,21 * * *` (UTC). It exits after reading the public deployed refresh report, and reads GitHub's public run status only if that report is stale, incomplete, or unavailable. There is no five-minute timer or retry loop (`restartPolicyType: NEVER`), no supplier scan, no workflow dispatch, and no GitHub token. GitHub Actions continues to own the full catalog/rate refresh.

The monitor reports `published`, `refresh_in_progress`, `stale`, `incomplete`, or `unavailable` in its runtime logs. An incomplete/failed snapshot cannot appear healthy just because a workflow ran. The prior authenticated dispatch helper remains in the repository for explicit future use; it is not the deployed function.
