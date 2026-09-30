/**
 * App Store / Play Store sheet when the shop is not running inside Nimiq Pay.
 */
import { Icon } from './Icon';
import { useT } from '../../i18n';
import { NIMIQ_PAY_IOS_URL, NIMIQ_PAY_ANDROID_URL } from '../../lib/miniapp';

function AppleIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true" fill="currentColor">
      <path d="M16.7 12.6c0-2.4 2-3.6 2.1-3.7-1.2-1.7-3-1.9-3.6-1.9-1.5-.2-3 .9-3.8.9s-2-.9-3.3-.9c-1.7 0-3.3 1-4.2 2.6-1.8 3.1-.5 7.7 1.3 10.2.9 1.2 1.9 2.6 3.3 2.5 1.3-.1 1.8-.8 3.4-.8s2 .8 3.4.8 2.2-1.3 3.1-2.5c1-.1.4-2.5 2.4-3.7-2.1-.3-2.5-2.4-2.5-2.5zm-2.4-7c.7-.9 1.2-2.1 1.1-3.3-1 .1-2.3.7-3 .1.6-1.2-1.6-2.4-2.7-2.4.1 1.2.5 2.4 1.2 3.3.7.9 1.9 1.6 3.1 1.5z" />
    </svg>
  );
}

function PlayIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true">
      <path fill="#34A853" d="M3.5 20.5 13.2 12 3.5 3.6v16.9z" />
      <path fill="#FBBC04" d="M16.7 8.5 13.2 12l3.5 3.5 4.2-2.4c.9-.5.9-1.8 0-2.3L16.7 8.5z" />
      <path fill="#4285F4" d="M3.5 3.6 13.2 12l3.5-3.5L5.6 2.1C4.5 1.5 3.5 2.2 3.5 3.6z" />
      <path fill="#EA4335" d="M13.2 12 3.5 20.5c0 1.4 1 2.1 2.1 1.5l11.1-6.4L13.2 12z" />
    </svg>
  );
}

export function NimiqPayInstallDialog({ onClose }: { onClose: () => void }) {
  const { t } = useT();
  return (
    <div
      className="overlay open"
      role="alertdialog"
      aria-modal="true"
      aria-label={t('login.payInstallTitle')}
      onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}
      style={{ zIndex: 80 }}
    >
      <div className="sheet" style={{ maxWidth: 460 }}>
        <div className="sheet-head" style={{ justifyContent: 'flex-end' }}>
          <h3 style={{ position: 'absolute', left: 0, right: 0, margin: 0, textAlign: 'center', whiteSpace: 'nowrap', pointerEvents: 'none' }}>
            {t('login.payInstallTitle')}
          </h3>
          <button className="sheet-close" aria-label={t('actions.close')} onClick={onClose} style={{ position: 'relative', zIndex: 1 }}>
            <Icon name="x" size={18} />
          </button>
        </div>
        <div className="sheet-body">
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', padding: '6px 0 0' }}>
            <div className="lock-ico">
              <Icon name="nimiq" size={34} />
            </div>
          </div>
          <p className="small muted center" style={{ margin: '10px 0 14px' }}>
            {t('login.payInstallHint')}
          </p>
          <div className="row" style={{ gap: '8px', flexWrap: 'wrap' }}>
            <a className="btn btn-outline" style={{ flex: '1', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', gap: '8px' }} href={NIMIQ_PAY_IOS_URL} target="_blank" rel="noopener noreferrer">
              <AppleIcon />
              <span className="btn-label">{t('checkout.lpAppStore')}</span>
            </a>
            <a className="btn btn-outline" style={{ flex: '1', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', gap: '8px' }} href={NIMIQ_PAY_ANDROID_URL} target="_blank" rel="noopener noreferrer">
              <PlayIcon />
              <span className="btn-label">{t('checkout.lpGooglePlay')}</span>
            </a>
          </div>
        </div>
      </div>
    </div>
  );
}
