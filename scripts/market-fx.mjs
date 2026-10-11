/** Validate fiat rates at the provider boundary; never label a baseline as live. */
const MIN_RATE = 1e-10;
const MAX_RATE = 100000;

function cleanTable(rates) {
  const clean = {};
  for (const [code, value] of Object.entries(rates || {})) {
    if (/^[A-Z]{3}$/.test(code) && typeof value === 'number' && Number.isFinite(value) && value >= MIN_RATE && value <= MAX_RATE) clean[code] = value;
  }
  if (Object.keys(clean).length < 50 || Math.abs((clean.USD || 0) - 1) > 1e-9 || !clean.TRY) throw new Error('FX response is incomplete or has an invalid USD base');
  clean.USD = 1;
  return clean;
}

function observation(value, now) {
  const at = typeof value === 'number' ? value : Date.parse(value);
  if (!Number.isFinite(at) || at <= 0 || at > now + 5 * 60_000 || now - at > 7 * 86400_000) throw new Error('FX observation timestamp is invalid or too old');
  return new Date(at).toISOString();
}

export function providerFX(payload, now = Date.now(), source = 'open.er-api.com') {
  if (payload?.result !== 'success' || payload?.base_code !== 'USD' || typeof payload?.time_last_update_unix !== 'number') throw new Error('FX feed must be a successful timestamped USD response');
  const rates = {};
  for (const [code, value] of Object.entries(payload.rates || {})) {
    if (typeof value === 'number' && Number.isFinite(value) && value > 0) rates[code] = 1 / value;
  }
  return {
    rates: cleanTable(rates),
    at: observation(payload.time_last_update_unix * 1000, now),
    source,
    via: 'feed',
  };
}

export function backendFX(payload, now = Date.now()) {
  if (payload?.base !== 'USD' || !payload.live_source || payload.live_source === 'embedded' || !(payload.live_currencies >= 50)) throw new Error('Backend FX is still using the embedded baseline');
  return { rates: cleanTable(payload.usd_per_unit), at: observation(payload.live_observed_at, now), source: payload.live_source, via: 'shopapi' };
}

export function fxSnapshot(live, baseline, previous, checkedAt) {
  const priorAt = Date.parse(previous?.live_observed_at || '');
  if (Number.isFinite(priorAt) && Date.parse(live.at) < priorAt) throw new Error('FX feed would replace a newer snapshot');
  const fallback = Object.keys(baseline).filter(code => live.rates[code] === undefined);
  return {
    base: 'USD', usd_per_unit: { ...baseline, ...live.rates },
    live_observed_at: live.at, live_source: live.source,
    live_currencies: Object.keys(live.rates).length,
    live_age_seconds: Math.max(0, Math.floor((Date.parse(checkedAt) - Date.parse(live.at)) / 1000)),
    fallback_currencies: fallback, generated_at: checkedAt, sync: 'static-hourly',
  };
}
