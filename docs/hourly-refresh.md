# Catalog and market refresh operations

The public `/status` page reads the report inside the **served snapshot**, not
the repository seed files. The footer links to this page. Each refreshed
snapshot contains the main GitHub run ID, catalog time, country failures,
rotating stock check counts, crypto provider observations and fiat provider
date. An Actions success without new served data cannot make the page fresh.

`catalog-refresh.yml` checks at minutes 17, 37 and 57, and avoids repeating a
successful published refresh less than 55 minutes old. Explicit manual runs
still force a refresh. `source=railway-watchdog` dispatches use the same guard
to avoid races with native scheduled jobs. All snapshots are artifacts; there
is only `main`, and no data branches or pull requests.

## Independent Railway watchdog (prepared, activation pending)

Source: `scripts/catalog-watchdog.mjs`; tests: `scripts/test-catalog-watchdog.mjs`.
Staged function: `catalog-watchdog`, service `4abed1e3-467c-41e0-a7c4-57dddf007a8f`,
production environment `3120ff98-7eab-40d2-81b2-1429e981917c`.
The function is **not deployed or scheduled yet**. It has no credential.

Intended service config: cron `*/5 * * * *`, restart policy `NEVER`, no public
domain. Every check exits. It dispatches only this repository's main catalog
workflow when fresh successful refresh data is absent. Recent queued/running
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

Fiat rates may have daily provider observations even though checked hourly.
Stock checks rotate under provider request limits; pending stock is visible
in the report. Failed countries/rates retain prior data and their timestamps.
