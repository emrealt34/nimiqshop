import excludedPurchases from '../../backend/internal/activityvisibility/excluded-purchases.json' with { type: 'json' };

const excluded = new Set(excludedPurchases);

/** Shared exclusions also protect clients receiving a pre-deployment API cache. */
export function publicActivityItems(items: readonly any[]): any[] {
  return items.filter(item => item && !excluded.has(String(item.id || '')) && (item.test_mode !== true || item.test_activity_visible === true));
}
