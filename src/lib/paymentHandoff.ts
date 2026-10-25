/**
 * paymentHandoff.ts — "copy the Nimiq Pay payment request", in the only order
 * that actually works in a browser.
 *
 * THE BUG THIS EXISTS FOR (owner, 2026-10-25: "nimiq pay ödeme talebini
 * kopyala … çalışmıyor"):
 *
 * The copy button used to run the same server round trip as the pay button
 * BEFORE touching the clipboard:
 *
 *     await authorizePaymentLaunch(quoteId)   // POST …/payment-launch
 *     Clipboard.copy(invoice)                 // ← execCommand('copy')
 *
 * `document.execCommand('copy')` is only allowed while the click's transient
 * user activation is alive: about five seconds in Chromium and far less
 * reliable in WebKit, where an `await` — let alone a network round trip — ends
 * the gesture. So the copy silently came back `false` exactly when the
 * supplier GET behind that POST was slow, and the buyer got "Kopyalama
 * tamamlanmadı" instead of the invoice. Worse, when the POST answered 4xx
 * (auth expired, order held, supplier unreachable) the handler threw before
 * the copy ever ran: the one situation where a buyer needs the invoice by hand
 * was the one where the shop refused to hand it over.
 *
 * The rules, therefore:
 *
 *   1. COPY FIRST, SYNCHRONOUSLY, inside the click handler. The invoice is
 *      already on the page — it is public, single-use data, and the clipboard
 *      is the device's, not the server's. Nothing may be awaited before it.
 *   2. STAMP AFTERWARDS. `payment_launch` is an audit stamp (backend:
 *      ClaimPaymentHandoff), never a permission gate for copying. It runs once
 *      the text is already in the clipboard, fire-and-forget, and its failure
 *      is swallowed: an unreachable server can never turn a completed copy
 *      into an error message.
 *   3. SAY THE TRUTH. `copied` / `failed` / `empty` are distinct outcomes, so
 *      the toast can only claim "copied" after a real copy.
 */

export type PaymentRequestCopyOutcome = 'copied' | 'failed' | 'empty';

export type PaymentRequestCopyPorts = {
  /**
   * Writes to the device clipboard. MUST be synchronous and return whether the
   * copy really happened (see lib/clipboard.ts — Clipboard.copy). An async
   * implementation (navigator.clipboard) would reintroduce the bug.
   */
  copy: (text: string) => boolean;
  /**
   * Audit / bookkeeping that must never delay or break the copy: the
   * fire-and-forget `payment-launch` stamp and whatever the caller wants to
   * remember locally. Only called after a successful copy.
   */
  stamp?: () => unknown;
};

/**
 * Copies the payment request (the BOLT11 invoice) and returns what really
 * happened. Synchronous by design: the caller's click handler calls this as
 * its first statement.
 */
export function copyPaymentRequest(invoice: string, ports: PaymentRequestCopyPorts): PaymentRequestCopyOutcome {
  const text = String(invoice ?? '').trim();
  // Nothing to copy: never claim success on an empty clipboard entry.
  if (!text) return 'empty';

  // 1. The copy — first, and still inside the user gesture.
  const ok = ports.copy(text);
  if (!ok) return 'failed';

  // 2. The stamp — afterwards, fire-and-forget. A rejected promise is as
  //    harmless as a thrown error here: the invoice is already copied.
  if (ports.stamp) {
    try {
      const out = ports.stamp();
      if (out && typeof (out as Promise<unknown>).catch === 'function') {
        void (out as Promise<unknown>).catch(() => {});
      }
    } catch {
      /* audit is not the copy */
    }
  }

  return 'copied';
}
