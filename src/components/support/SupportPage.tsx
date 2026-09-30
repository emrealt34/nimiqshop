/**
 * SupportPage.tsx — Help & FAQ. The ticket inbox is retired: purchases are
 * fulfilled and delivered by CryptoRefills (the merchant of record), so every
 * code, replacement and refund question goes to the address the gift card was
 * emailed to. This page states that plainly, answers the common cases and
 * leaves one contact email for everything else. No API calls, no writes.
 */
import React from 'react';
import { Icon } from '../ui/Icon';
import { AppRoot } from '../AppRoot';
import { AlertBox, CopyButton } from '../ui/uiKit';
import { pagePath } from '../../lib/asset';

const CONTACT_EMAIL = 'support@nimiqbase.com';

type Faq = { q: string; body: React.ReactNode };

const FAQS: Faq[] = [
  {
    q: 'My code is not working — what do I do?',
    body: (
      <>
        Gift cards are generated and emailed <strong>directly by CryptoRefills</strong>, our supplier — not by
        this shop. Email CryptoRefills support <strong>from the exact address the gift card was delivered to</strong>
        (check your spam/junk folder first). Include your order id and the product name; they can verify delivery
        and re-issue a working code.
      </>
    ),
  },
  {
    q: 'I never received the gift card email',
    body: (
      <>
        The card goes to the <strong>delivery email you entered at checkout</strong> — it may differ from your
        contact email. Digital cards usually arrive within a few minutes. Search your inbox and spam for
        “CryptoRefills”, and if it is missing, email CryptoRefills support from that address with your order id.
      </>
    ),
  },
  {
    q: 'How do refunds, replacements and cancellations work?',
    body: (
      <>
        <strong>CryptoRefills is the merchant of record</strong> for every purchase: the card, the invoice and the
        after-sales process are theirs. Refunds, cancellations and code replacements are handled exclusively by
        CryptoRefills — this shop is not responsible for them and cannot issue a refund on a completed purchase.
        Route the request to the address the gift card was sent to and they will resolve it.
      </>
    ),
  },
  {
    q: 'Where can I track my order?',
    body: (
      <>
        Every order has a public tracking page — no login needed. Open{' '}
        <a href={pagePath('/track')}>Track order</a> and paste your order id to see the live lifecycle
        (payment → supplier purchase → delivered). Signed in, your <a href={pagePath('/orders')}>orders list</a>{' '}
        shows the same statuses with payment details.
      </>
    ),
  },
  {
    q: 'I paid, but the order still says “awaiting payment”',
    body: (
      <>
        Payments are verified on-chain; a block or two of confirmation is normal before the order moves to
        “processing”. If your wallet shows the transfer as settled after 30 minutes, reopen the order from your{' '}
        <a href={pagePath('/orders')}>orders list</a> — it refreshes the status — and only then contact us with
        the transaction hash.
      </>
    ),
  },
  {
    q: 'When is my cashback paid?',
    body: (
      <>
        Cashback is credited <strong>after CryptoRefills marks your order fulfilled</strong> — never before the
        card is delivered. It is paid in NIM to the wallet used at checkout (or to the tree-planting address if you
        chose donation). Live amounts are on the <a href={pagePath('/cashback')}>cashback page</a>.
      </>
    ),
  },
  {
    q: 'Am I looking at a duplicate charge / two orders for one cart?',
    body: (
      <>
        A payment that fails validation never creates a supplier order, and the shop refuses a second live order
        for the same cart — so a duplicate charge is extremely rare. If your wallet really shows two settled
        payments, email CryptoRefills with <strong>both</strong> order ids; only they can reconcile and refund a
        duplicate.
      </>
    ),
  },
];

function FaqItem({ faq, index }: { faq: Faq; index: number }) {
  return (
    <details className="card faq-item" style={{ padding: 0, overflow: 'hidden' }}>
      <summary
        className="row"
        style={{
          gap: 10, alignItems: 'center', cursor: 'pointer', listStyle: 'none',
          padding: '14px 16px', fontWeight: 600, fontSize: '0.98rem',
        }}
      >
        <span className="faq-num" style={{ flex: 'none', width: 22, height: 22, borderRadius: '50%', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', fontSize: 12, background: 'rgba(127,127,127,.14)' }}>{index + 1}</span>
        <span style={{ flex: 1 }}>{faq.q}</span>
        <Icon name="chevron" size={16} />
      </summary>
      <div style={{ padding: '0 16px 14px 48px', lineHeight: 1.55 }} className="small">
        {faq.body}
      </div>
    </details>
  );
}

export function SupportView() {
  return (
    <div className="container" style={{ maxWidth: 760 }}>
      <div style={{ margin: '6px 0 2px' }}>
        <h1 style={{ display: 'flex', alignItems: 'center', gap: 10, margin: 0 }}>
          <Icon name="headset" size={24} /> Help &amp; FAQ
        </h1>
        <p className="lede" style={{ margin: '6px 0 14px' }}>
          Quick answers about gift codes, delivery and what happens after checkout.
        </p>
      </div>

      <AlertBox type="warn">
        <strong>Who to contact about your card:</strong> gift codes are delivered, replaced and refunded by{' '}
        <strong>CryptoRefills</strong>, the merchant of record — write to them from{' '}
        <strong>the email address the gift card was sent to</strong>. This shop does not process refunds or
        purchases made through the supplier.
      </AlertBox>

      <div className="mt-2" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {FAQS.map((faq, i) => <FaqItem key={faq.q} faq={faq} index={i} />)}
      </div>

      <div className="card mt-2">
        <div className="card-title"><Icon name="mail" size={16} /> Contact the shop</div>
        <div className="small" style={{ lineHeight: 1.55 }}>
          For anything the FAQ could not settle — order lookups, cashback status, payments, bugs on this site —
          write to us. We cannot refund supplier purchases for you, but we will always help you find your order
          and the right CryptoRefills channel.
        </div>
        <div className="row mt-2" style={{ gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <a className="btn btn-gold btn-sm" href={`mailto:${CONTACT_EMAIL}`}>
            <Icon name="send" size={14} /> {CONTACT_EMAIL}
          </a>
          <CopyButton getText={CONTACT_EMAIL} label="" />
        </div>
      </div>

      <p className="xs faint mt-2" style={{ textAlign: 'center' }}>
        There is no ticket inbox any more — email is the only channel, and answers take up to 2 business days.
      </p>
    </div>
  );
}

export function SupportPage() {
  return (
    <AppRoot activeKey="support">
      <SupportView />
    </AppRoot>
  );
}
