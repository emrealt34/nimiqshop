/**
 * clipboard.ts — the site's ONE clipboard. Started as a 1:1 port of Nimiq's
 * `@nimiq/utils/clipboard` (v0.12.4, dist/clipboard/Clipboard.mjs); it keeps
 * that call shape, but the copy itself is now hardened for the browsers the
 * SHOP runs in (owner, 2026-11-11: "ödeme ekranında kopyala butonu var ya,
 * çalışmıyor" — the button was pressed and nothing happened).
 *
 *   import { Clipboard } from './clipboard'
 *   if (Clipboard.copy(address)) { say('copied') }
 *   else { say('copy failed — here is the text to select') }
 *
 * WHY THIS IMPLEMENTATION (why not plain navigator.clipboard)
 * ----------------------------------------------------------
 * `navigator.clipboard.writeText` is asynchronous, needs a secure context +
 * permissions, and is missing or rejected on a number of mobile WebViews —
 * precisely where a shopper pays. The synchronous path is a hidden field +
 * `execCommand('copy')` inside the click handler, so the write happens while
 * the click's user activation is still alive.
 *
 * WHAT WAS ACTUALLY BROKEN (the three real-world holes)
 * ----------------------------------------------------
 *  1. The field carried `contain: strict`. Size containment collapses an
 *     absolutely-positioned textarea to 0×0, and WebKit then refuses to build
 *     a selection inside it: `execCommand('copy')` answers `true`, the
 *     clipboard stays EMPTY. The old helper field is now a real 1×1 off-screen
 *     box with `opacity: 0` — invisible, zero layout impact, still selectable.
 *  2. `element.select()` was called without focusing first. iOS only builds
 *     the selection on a focused field, so the same empty-copy happened there.
 *     The field is focused (with `preventScroll`) before selecting.
 *  3. `execCommand` was the ONLY strategy. Where a WebView has removed it, or
 *     a permissions policy blocks it, the copy failed with no second chance.
 *     There are now three, in order: hidden textarea → `contentEditable`
 *     range (the iOS recipe) → `navigator.clipboard` (async, see `write`).
 *
 * The helper ALWAYS returns a boolean, so the UI can say the truth: a button
 * that reports "copied" only after a copy, and an honest failure message
 * instead of a dead control.
 */

/** Save the buyer's selection + focus, run `write` with `helper` in the
 *  document, and put the page back exactly as it was. The restore runs in a
 *  `finally` so a throwing strategy can never leave the helper behind or eat
 *  the caret mid-checkout. */
function withSelectionPreserved<T>(helper: HTMLElement, write: () => T): T {
    const selection = typeof document.getSelection === 'function' ? document.getSelection() : null;
    const originalRange = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null;
    const activeElement = document.activeElement instanceof HTMLElement ? document.activeElement : null;

    document.body.append(helper);
    try {
        return write();
    } finally {
        helper.remove();
        // `isConnected` guards the case where the focused control has already
        // unmounted (a 5-second poll re-rendered the card under the click).
        if (activeElement && activeElement.isConnected) {
            activeElement.focus({ preventScroll: true });
        }
        if (originalRange && selection
            && !(activeElement instanceof HTMLInputElement || activeElement instanceof HTMLTextAreaElement)
        ) {
            selection.removeAllRanges();
            selection.addRange(originalRange);
        }
    }
}

/** Give a helper element the shared geometry: off-screen, 1×1, transparent —
 *  12pt is the iOS Safari value that keeps the OS from zooming into it. */
function placeHelper(element: HTMLElement): void {
    const style = element.style;
    style.position = 'absolute';
    style.top = '0';
    style.left = '-9999px';
    style.width = '1px';
    style.height = '1px';
    style.padding = '0';
    style.border = '0';
    style.margin = '0';
    style.overflow = 'hidden';
    style.opacity = '0';
    style.fontSize = '12pt';
    // Never interactive: a stray tap must not land on the helper.
    style.pointerEvents = 'none';
}

function runCopyCommand(): boolean {
    try {
        return document.execCommand('copy');
    } catch {
        // Some engines throw instead of returning false; that is still just
        // a failed copy, and the next strategy gets its turn.
        return false;
    }
}

/** Strategy 1 — the hidden textarea (works in every desktop browser). */
function copyViaTextarea(value: string): boolean {
    const element = document.createElement('textarea');
    element.value = value;
    element.setAttribute('readonly', '');
    element.setAttribute('aria-hidden', 'true');
    element.setAttribute('tabindex', '-1');
    placeHelper(element);
    return withSelectionPreserved(element, () => {
        // Focus BEFORE selecting: WebKit only builds the selection on focus.
        element.focus({ preventScroll: true });
        element.select();
        element.setSelectionRange(0, value.length);
        return runCopyCommand();
    });
}

/** Strategy 2 — an `contentEditable` range (the classic iOS Safari recipe,
 *  used when the textarea produced no selection). */
function copyViaContentEditable(value: string): boolean {
    const element = document.createElement('div');
    // textContent, never innerHTML: the invoice is API data, not markup.
    element.textContent = value;
    element.setAttribute('contenteditable', 'true');
    element.setAttribute('aria-hidden', 'true');
    placeHelper(element);
    return withSelectionPreserved(element, () => {
        const selection = typeof document.getSelection === 'function' ? document.getSelection() : null;
        if (!selection || typeof document.createRange !== 'function') return false;
        element.focus({ preventScroll: true });
        const range = document.createRange();
        range.selectNodeContents(element);
        selection.removeAllRanges();
        selection.addRange(range);
        return runCopyCommand();
    });
}

/** Utility for copying text to the clipboard with mobile compatibility. */
export class Clipboard {
    /**
     * Copies text to the clipboard, synchronously, inside the caller's click.
     *
     * @param text - The text to copy.
     * @returns `true` if a copy strategy really reported success, `false`
     *          when every strategy failed or there is no DOM to copy with.
     */
    public static copy(text: string): boolean {
        // No document (SSR, worker): nothing can be copied.
        if (typeof globalThis.document === 'undefined') return false;
        const value = String(text ?? '');
        if (!value) return false;

        if (copyViaTextarea(value)) return true;
        return copyViaContentEditable(value);
    }

    /**
     * Copies text through the asynchronous Clipboard API, falling back to the
     * synchronous path. Callers use this AFTER `copy()` returned `false`: the
     * async write still carries the click's activation, and it is the only
     * strategy that works in the WebViews where `execCommand` is refused.
     *
     * @returns `true` only when the text really reached the clipboard.
     */
    public static async write(text: string): Promise<boolean> {
        const value = String(text ?? '');
        if (!value) return false;

        const nav = (globalThis as { navigator?: Navigator }).navigator as
            | (Navigator & { clipboard?: { writeText?: (value: string) => Promise<void> } })
            | undefined;
        const clipboard = nav && nav.clipboard;
        if (clipboard && typeof clipboard.writeText === 'function') {
            try {
                await clipboard.writeText(value);
                return true;
            } catch {
                // Permission / permissions-policy / gesture refused: the
                // synchronous strategies may still deliver.
            }
        }
        return Clipboard.copy(value);
    }
}
