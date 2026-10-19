/**
 * AddressChip.tsx — one wallet, shown the way an operator recognises it: the
 * address identicon (a face, not a string of letters) next to the address in
 * GROUPS OF FOUR — NQ24 2XCD PK1C 7DR5 … — which is how every Nimiq wallet
 * writes it and the only way two addresses can be compared by eye without
 * counting characters.
 *
 * Owner (2026-10-19): "herkesi avatarlarıyla ve 4 hane ayrılmış şekilde
 * görmek siterdim" — in the quiz participants, the staker ledgers, the admin
 * orders page and the ratings. One component so all four stay identical and
 * a fifth place costs one line.
 *
 * The face is the identicon, never the raw string: two rows that look alike
 * at a glance are then obviously different people.
 */
import { Identicon } from './Identicon';
import { CopyButton } from './uiKit';
import { formatWalletAddress } from '../../lib/format';

export function AddressChip({
  address,
  fallback,
  size = 22,
  copy = false,
  label,
}: {
  /** The Nimiq address (NQ…). Empty or non-address values fall back to text. */
  address?: string | null;
  /** Shown when there is no address (e.g. a user id). Never given a face. */
  fallback?: string | null;
  size?: number;
  copy?: boolean;
  /** A short prefix, e.g. "Wallet". */
  label?: string;
}) {
  const raw = String(address || '').trim();
  const isAddress = /^(NQ|nq)[0-9A-Za-z]{20,}$/.test(raw.replace(/\s+/g, ''));
  // Only an ADDRESS is regrouped. A user id or any other label is shown
  // exactly as it came: chopping "user-a1b2c3" into "user -a1b 2c3" invents
  // a wallet shape that is not there.
  const shown = raw ? (isAddress ? formatWalletAddress(raw, 4) : raw) : String(fallback || '—');
  return (
    <span
      className="addr-chip"
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        minWidth: 0,
        maxWidth: '100%',
        verticalAlign: 'middle',
      }}
    >
      {isAddress ? (
        <Identicon
          address={raw}
          size={size}
          className="identicon addr-chip-face"
        />
      ) : null}
      {label ? <span className="faint">{label}</span> : null}
      <code
        className="mono"
        style={{ overflowWrap: 'anywhere', fontSize: 12 }}
        title={raw || undefined}
      >
        {shown}
      </code>
      {copy && raw ? <CopyButton getText={raw} /> : null}
    </span>
  );
}

export default AddressChip;
