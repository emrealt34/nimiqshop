// Completion describes this refresh, independently of later display freshness.
export function refreshCompletion(catalog, market, now = Date.now(), outcomes = {}) {
  const recent = (value, maxAge) => Number.isFinite(Date.parse(value))
    && now >= Date.parse(value) && now - Date.parse(value) < maxAge;
  const positive = value => Number.isFinite(value) && value > 0;
  const succeeded = outcome => outcome === undefined || outcome === 'success';
  const ratesComplete = succeeded(outcomes.market) && recent(market?.generated_at, 3600_000)
    && recent(market?.nim?.observed_at, 3600_000) && recent(market?.btc?.observed_at, 3600_000)
    && positive(market?.nim?.usd_per_nim) && positive(market?.btc?.usd_per_btc)
    // The fiat feed publishes daily; its provider time must remain intact.
    && recent(market?.fx?.observed_at, 7 * 86400_000) && positive(market?.fx?.currencies);
  const stock = catalog?.verified;
  const catalogComplete = succeeded(outcomes.catalog) && catalog?.complete === true && stock?.mode === 'full'
    && stock.completed === true && stock.skipped === 0 && stock.unresolved === 0
    && !(catalog.failed?.length) && recent(catalog.completed_at, 3600_000);
  return { complete: catalogComplete && ratesComplete, ratesComplete };
}
