/**
 * clipboard.ts — the site's ONE clipboard, ported 1:1 from Nimiq's own
 * `@nimiq/utils/clipboard` (v0.12.4, dist/clipboard/Clipboard.mjs), so every
 * copy on this site behaves exactly like Nimiq Pay's:
 *
 *   import { Clipboard } from '@nimiq/utils/clipboard'
 *   if (Clipboard.copy(address)) { console.log('Address copied to clipboard!') }
 *   else { console.log('Copy failed - try again') }
 *
 * WHY THIS IMPLEMENTATION (why not navigator.clipboard)
 * ----------------------------------------------------
 * `navigator.clipboard.writeText` is asynchronous, needs a secure context +
 * permissions, and is missing or rejected on a number of mobile WebViews —
 * precisely where a shopper pays. The Nimiq path is a synchronous
 * textarea + execCommand('copy') inside the click handler:
 *
 *   - `readonly` + `contain: strict` + off-screen absolute positioning keep
 *     the page from scrolling/zooming while the helper textarea is selected;
 *   - `font-size: 12pt` (≥16px is not needed, but 12pt is the classic iOS
 *     Safari value) stops iOS from auto-zooming into the field;
 *   - the user's previous selection AND focus are saved before and restored
 *     after, so copying never eats the caret mid-checkout;
 *   - it returns a boolean — the caller can SAY when the copy failed instead
 *     of hoping.
 *
 * Nimiq Pay itself uses this util for address copying inside its webviews;
 * our Lightning invoice / USDT address / share-text copies now run the exact
 * same code.
 */

/** Utility for copying text to the clipboard with mobile compatibility. */
export class Clipboard {
    /**
     * Copies text to the clipboard.
     *
     * @param text - The text to copy.
     * @returns `true` if the copy succeeded, `false` otherwise (including
     *          when there is no DOM to copy with).
     */
    public static copy(text: string): boolean {
        // No document (SSR, worker): nothing can be copied.
        if (typeof globalThis.document === 'undefined') return false;

        const element = document.createElement('textarea');
        element.value = text;
        element.setAttribute('readonly', '');
        element.style.contain = 'strict';
        element.style.position = 'absolute';
        element.style.left = '-9999px';
        // 12pt is the iOS Safari value: keeps the OS from zooming into the
        // hidden field when it receives focus/selection.
        element.style.fontSize = '12pt';

        // Save the user's current selection and focus so the copy leaves the
        // page exactly as it found it.
        const selection = document.getSelection();
        const originalRange = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null;
        const activeElement = document.activeElement instanceof HTMLElement ? document.activeElement : null;

        document.body.append(element);
        element.select();
        element.selectionStart = 0;
        element.selectionEnd = text.length;

        let isSuccess = false;
        try {
            isSuccess = document.execCommand('copy');
        } catch (e) {
            // Some engines throw instead of returning false; that is still
            // just a failed copy.
        }

        element.remove();

        if (activeElement) {
            activeElement.focus();
        }
        if (originalRange && selection
            && !(activeElement instanceof HTMLInputElement || activeElement instanceof HTMLTextAreaElement)
        ) {
            selection.removeAllRanges();
            selection.addRange(originalRange);
        }

        return isSuccess;
    }
}
