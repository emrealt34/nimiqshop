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

## Independent Railway watchdog (prepared, activation pending)

Source: `scripts/catalog-watchdog.mjs`; tests: `scripts/test-catalog-watchdog.mjs`.
Staged function: `catalog-watchdog`, service `4abed1e3-467c-41e0-a7c4-57dddf007a8f`,
production environment `3120ff98-7eab-40d2-81b2-1429e981917c`.
The function is **not deployed or scheduled yet**. It has no credential.

Intended service config: cron `*/5 * * * *`, restart policy `NEVER`, no public
domain. Every check exits. It dispatches only this repository's main catalog
workflow when six-hour successful refresh data is absent. Recent queued/running
work is left alone. The script never calls suppliers, sends payments, or needs
Cloudflare/deployment secrets.

Activation requires a service-specific `CATALOG_GITHUB_TOKEN` secret. Prefer a
fine-grained token limited to `emrealt34/nimiqshop`, Actions read/write and
metadata read. **Do not embed credentials in code or commit them.** Automatic
approval review rejected persisting the existing broad PAT in Railway because
that exact durable access was not authorized. Leave activation pending until
the owner explicitly approves the secret storage or provides scoped access.

After activation, verify Railway's cron execution timestamp/result and logs,
then inspect the corresponding GitHub refresh job, deployment, and `/status`.
A successful manual refresh is not evidence that either scheduler is working.

Fiat rates may have daily provider observations even though checked every six hours.
Stock checks cover all families each scheduled run; pending stock is visible
in the report. Failed countries/rates retain prior data and their timestamps.

The staged Railway function must be updated from the current main script before activation; it remains undeployed and has no credential.
