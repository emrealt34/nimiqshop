/**
 * AppProviders.tsx — global UI providers: toasts + bottom sheets, plus a
 * session listener. This is the React analogue of the imperative
 * openSheet/closeSheet/toast() helpers in the original ui.js. Components use
 * the exported hooks (useToast, useSheet) instead of imperative calls.
 *
 * Customer-facing copy (toast dismiss, sheet close, error boundary) comes
 * from the i18n layer so it auto-translates with the rest of the UI.
 */
import { Component, createContext, useCallback, useContext, useEffect, useRef, useState, type ErrorInfo, type ReactNode } from 'react';
import { Icon } from './ui/Icon';
import { ErrorDetail } from './ui/uiKit';
import { siteName } from '../lib/config';
import { useT, type Translator } from '../i18n';

/* ---------------- Toasts ---------------- */
type ToastKind = 'success' | 'error' | 'info' | 'warn';
type Toast = { id: number; text: string; kind: ToastKind; avatar?: ReactNode };

type ToastCtx = {
  toast: (text: string, kind?: ToastKind, avatar?: ReactNode) => void;
};
const ToastContext = createContext<ToastCtx>({ toast: () => {} });
export const useToast = () => useContext(ToastContext);

function ToastView({ t: toast, onDone }: { t: Toast; onDone: () => void }) {
  // ToastView renders inside the I18nProvider (because AppProviders wraps it),
  // so useT() is available here.
  const { t } = useT();
  return (
    <div
      className={`toast toast-${toast.kind}`}
      role="status"
      onClick={onDone}
      style={{ display: 'flex', alignItems: 'center', gap: '8px' }}
    >
      {toast.avatar}
      <span>{toast.text}</span>
      <button type="button" className="toast-dismiss" aria-label={t('toast.dismiss')} onClick={(event) => { event.stopPropagation(); onDone(); }}>
        <Icon name="x" size={14} />
      </button>
    </div>
  );
}

/* ---------------- Sheets ---------------- */

/** A sheet heading: a ready string, or a resolver that translates at render
 * time. The resolver exists because a sheet OUTLIVES the render pass that
 * opened it: a caller that captured `t(...)` before the locale dictionary for
 * the visitor's language had arrived would otherwise show that heading — and
 * the dialog's accessible name — in the fallback language forever. */
export type SheetTitle = string | ((t: Translator) => string);

type SheetState = {
  id: number;
  title: SheetTitle;
  wide?: boolean;
  render: (close: () => void) => ReactNode;
};
type SheetCtx = {
  openSheet: (opts: { title: SheetTitle; wide?: boolean; render: SheetState['render'] }) => number;
  closeSheet: (id?: number) => void;
};
const SheetContext = createContext<SheetCtx>({ openSheet: () => 0, closeSheet: () => {} });
export const useSheet = () => useContext(SheetContext);

/**
 * ErrorBoundary class still needed (React error boundaries must be classes),
 * but the rendered message uses <ErrorFallback /> which is a functional
 * component that reads from i18n. That way the crash screen, which renders
 * when the component tree has broken, still shows translated copy.
 */
class ErrorBoundary extends Component<{ children: ReactNode }, { err: Error | null }> {
  state: { err: Error | null } = { err: null };
  static getDerivedStateFromError(err: Error) { return { err }; }
  componentDidCatch(err: Error, info: ErrorInfo) {
    try { console.error(siteName() + ' crashed', err, info); } catch {}
  }
  render() {
    if (!this.state.err) return this.props.children;
    // The fallback keeps its calm copy, and carries the actual error underneath
    // (owner, 2026-10-05: "böyle şeyleri" — a screen that only says "something
    // went wrong" gives the buyer and support nothing to work with).
    return <ErrorFallback detail={String(this.state.err?.message || '')} />;
  }
}

function ErrorFallback({ detail = '' }: { detail?: string }) {
  const { t } = useT();
  return (
    <div className="container" style={{ padding: '40px 16px', textAlign: 'center' }}>
      <div className="strong" style={{ fontSize: '1.2rem' }}>{t('errors.wentWrong')}</div>
      <p className="small muted mt-1" style={{ maxWidth: 420, margin: '8px auto 0' }}>{t('errors.crashedBody')}</p>
      {detail ? <ErrorDetail detail={detail} /> : null}
      <button className="btn btn-gold mt-2" type="button" onClick={() => window.location.reload()}>
        {t('errors.reload')}
      </button>
    </div>
  );
}

export function AppProviders({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [sheets, setSheets] = useState<SheetState[]>([]);
  const toastId = useRef(0);
  const sheetId = useRef(0);
  const contentRef = useRef<HTMLDivElement>(null);
  const hasSheets = sheets.length > 0;

  useEffect(() => {
    if (!hasSheets) return;
    const overflow = document.body.style.overflow;
    const content = contentRef.current;
    const background = [document.querySelector('footer'), document.getElementById('tabbar-root')]
      .filter((node): node is HTMLElement => !!node).map((node) => ({ node, wasInert: node.hasAttribute('inert') }));
    document.body.style.overflow = 'hidden';
    content?.setAttribute('inert', '');
    background.forEach(({ node }) => node.setAttribute('inert', ''));
    return () => {
      document.body.style.overflow = overflow;
      content?.removeAttribute('inert');
      background.forEach(({ node, wasInert }) => { if (!wasInert) node.removeAttribute('inert'); });
    };
  }, [hasSheets]);

  const toast = useCallback((text: string, kind: ToastKind = 'info', avatar?: ReactNode) => {
    const id = ++toastId.current;
    setToasts((prev) => [...prev, { id, text, kind, avatar }]);
    // DS172411 (setTimeout): closure only, never a string — no untrusted data is evaluated.
    setTimeout(() => setToasts((prev) => prev.filter((t) => t.id !== id)), 3500);
  }, []);

  const openSheet = useCallback((opts: { title: SheetTitle; wide?: boolean; render: SheetState['render'] }) => {
    const id = ++sheetId.current;
    setSheets((prev) => [...prev, { id, title: opts.title, wide: opts.wide, render: opts.render }]);
    return id;
  }, []);

  const closeSheet = useCallback((id?: number) => {
    setSheets((prev) => (id ? prev.filter((s) => s.id !== id) : prev.slice(0, -1)));
  }, []);

  // Expose imperative fallbacks on window (the original code used them via
  // openSheet/toast imports; keeping parity in case any vanilla module remains).
  useEffect(() => {
    const w = window as any;
    w.__nimshop = { ...w.__nimshop, openSheet, closeSheet, toast };
  }, [openSheet, closeSheet, toast]);

  return (
    <ToastContext.Provider value={{ toast }}>
      <SheetContext.Provider value={{ openSheet, closeSheet }}>
        <div ref={contentRef}><ErrorBoundary>{children}</ErrorBoundary></div>
        {/* Toast stack */}
        {toasts.length > 0 && (
          <div className="toast-stack">
            {toasts.map((t) => (
              <ToastView key={t.id} t={t} onDone={() => setToasts((prev) => prev.filter((x) => x.id !== t.id))} />
            ))}
          </div>
        )}
        {/* Sheets */}
        {sheets.map((s, index) => (
          <SheetView key={s.id} sheet={s} isTop={index === sheets.length - 1} onClose={() => closeSheet(s.id)} />
        ))}
      </SheetContext.Provider>
    </ToastContext.Provider>
  );
}

function SheetView({ sheet, onClose, isTop }: { sheet: SheetState; onClose: () => void; isTop: boolean }) {
  const { t } = useT();
  // Re-resolve the heading on every render, so a language change (or the
  // arrival of the language's dictionary chunk) updates both the visible title
  // and the dialog's accessible name.
  const title = typeof sheet.title === 'function' ? sheet.title(t) : sheet.title;
  const dialogRef = useRef<HTMLDivElement>(null);
  const openerRef = useRef<HTMLElement | null>(typeof document === 'undefined' ? null : document.activeElement as HTMLElement);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    dialog.toggleAttribute('inert', !isTop);
    if (!isTop) return;
    const previous = openerRef.current;
    const focusable = () => Array.from(dialog.querySelectorAll<HTMLElement>(
      'a[href], button, input, select, textarea, [tabindex]'
    )).filter((el) => el.tabIndex >= 0 && !el.matches(':disabled') && !el.closest('[hidden], [inert]') &&
      getComputedStyle(el).display !== 'none' && getComputedStyle(el).visibility !== 'hidden');
    dialog.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        closeRef.current();
      } else if (event.key === 'Tab') {
        const elements = focusable();
        const first = elements[0];
        const last = elements[elements.length - 1];
        if (!first) { event.preventDefault(); dialog.focus(); return; }
        if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog)) {
          event.preventDefault(); last.focus();
        } else if (!event.shiftKey && (document.activeElement === last || !dialog.contains(document.activeElement))) {
          event.preventDefault(); first.focus();
        }
      }
    };
    const containFocus = (event: FocusEvent) => {
      if (!dialog.contains(event.target as Node)) dialog.focus();
    };
    document.addEventListener('keydown', onKey);
    document.addEventListener('focusin', containFocus);
    return () => {
      document.removeEventListener('keydown', onKey);
      document.removeEventListener('focusin', containFocus);
      // Wait until the uncovered sheet/background has had inert removed.
      queueMicrotask(() => { if (previous?.isConnected && !previous.closest('[inert]')) previous.focus(); });
    };
  }, [isTop]);

  return (
    <div className="sheet-backdrop" aria-hidden={!isTop || undefined} onClick={() => { if (isTop && !sheet.wide) onClose(); }}>
      <div
        ref={dialogRef}
        tabIndex={-1}
        className={`sheet${sheet.wide ? ' sheet-wide' : ''}`}
        role="dialog"
        aria-modal={isTop || undefined}
        aria-label={title}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="sheet-head">
          <div className="strong">{title}</div>
          <button type="button" className="sheet-close" aria-label={t('actions.close')} onClick={onClose}>
            <Icon name="x" size={20} />
          </button>
        </div>
        <div className="sheet-body">{sheet.render(onClose)}</div>
      </div>
    </div>
  );
}
