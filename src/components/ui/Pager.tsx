/**
 * Pager.tsx — the site's default pagination (the one "Recent cashback" uses).
 * Renders prev/next plus numbered page buttons (windowed, so huge page counts
 * stay tidy with "…"). Pass the current page (0-based) and the page count; it
 * hides itself when there is only one page.
 */
import { Icon } from './Icon';
import { useT } from '../../i18n';

/** Builds the list of page buttons to show, using "…" for gaps. Pages are 1-based here. */
function windowPages(page1: number, count: number): (number | '…')[] {
  if (count <= 7) return Array.from({ length: count }, (_, i) => i + 1);
  const pages = new Set<number>([1, count, page1, page1 - 1, page1 + 1]);
  for (const p of pages) {
    if (p < 1 || p > count) pages.delete(p);
  }
  const sorted = [...pages].sort((a, b) => a - b);
  const out: (number | '…')[] = [];
  let prev = 0;
  for (const p of sorted) {
    if (p - prev > 1) out.push('…');
    out.push(p);
    prev = p;
  }
  return out;
}

export function Pager({ page, pageCount, onPage }: { page: number; pageCount: number; onPage: (p: number) => void }) {
  const { t } = useT();
  if (pageCount <= 1) return null;
  const current = Math.max(0, Math.min(page, pageCount - 1)); // 0-based
  const items = windowPages(current + 1, pageCount); // 1-based window
  return (
    <nav className="pager mt-2" aria-label={t('ui.pagerAria')}>
      <button
        type="button"
        className="btn btn-sm pager-prev"
        disabled={current === 0}
        aria-label={t('ui.newerPage')}
        onClick={() => onPage(current - 1)}
      >
        <Icon name="chevron-down" size={14} style={{ transform: 'rotate(90deg)' }} />
      </button>
      {items.map((it, i) =>
        it === '…' ? (
          <span key={'gap' + i} className="pager-gap" aria-hidden="true">
            …
          </span>
        ) : (
          <button
            key={it}
            type="button"
            className={'btn btn-sm pager-num' + (it - 1 === current ? ' active' : '')}
            aria-current={it - 1 === current ? 'page' : undefined}
            aria-label={t('ui.pageNumber', { n: it })}
            onClick={() => onPage(it - 1)}
          >
            {it}
          </button>
        )
      )}
      <button
        type="button"
        className="btn btn-sm pager-next"
        disabled={current >= pageCount - 1}
        aria-label={t('ui.olderPage')}
        onClick={() => onPage(current + 1)}
      >
        <Icon name="chevron-down" size={14} style={{ transform: 'rotate(-90deg)' }} />
      </button>
    </nav>
  );
}
