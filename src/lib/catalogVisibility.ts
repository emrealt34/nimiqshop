/** The owner's permanent e-money/KYC exclusion, shared by UI and hourly CI. */
const slug = (value: unknown): string => String(value || '').trim().toLowerCase().replace(/[\s_-]+/g, '');
const required = (value: unknown): boolean => value === true || value === 1 || value === 'true' || value === '1';
const familyName = (row: any): string => {
  const name = String(row?.family || row?.family_name || row?.brand || '').trim();
  return (/^\[([^\]]+)\]/.exec(name)?.[1] || name).toLowerCase();
};
const familyKey = (row: any, country = ''): string => `${familyName(row)}|${String(row?.country_code || country).toUpperCase()}`;

export function excludedCatalogItem(row: any, category?: unknown): boolean {
  if (!row || typeof row !== 'object') return false;
  const categories = [category, row.category, ...(Array.isArray(row.additional_categories) ? row.additional_categories : [])];
  return categories.some(c => slug(c) === 'emoney')
    || slug(familyName(row)).includes('emoney')
    || required(row.is_e_money) || required(row.kyc_required) || required(row.requires_kyc);
}

/** Reject the whole family when ANY variant/category is excluded, before
 * kind filtering or duplicate-brand merging can lose that evidence. */
export function filterPublicCatalog(data: any): any {
  if (!data || !Array.isArray(data.categories)) return data;
  const blocked = new Set<string>();
  for (const cat of data.categories) {
    for (const row of cat.brands || []) {
      if (excludedCatalogItem(row, cat.category)) blocked.add(familyKey(row, data.country_code));
    }
  }
  return { ...data, categories: data.categories.flatMap((cat: any) => {
    if (slug(cat.category) === 'emoney') return [];
    const brands = (cat.brands || []).filter((row: any) => !excludedCatalogItem(row, cat.category)
      && !blocked.has(familyKey(row, data.country_code)))
      .map((row: any) => ({ ...row,
        ...(!row.category && cat.category ? { category: cat.category } : {}),
        ...(!row.kind && cat.kind ? { kind: cat.kind } : {}) }));
    return brands.length ? [{ ...cat, brands }] : [];
  }) };
}

export function filterPublicFamilies(data: any): any {
  const families = Array.isArray(data) ? data : data?.products ? [data] : [];
  if (families.some((row: any) => excludedCatalogItem(row)
    || (row.products || []).some((p: any) => excludedCatalogItem(p)))) return [];
  return data;
}
