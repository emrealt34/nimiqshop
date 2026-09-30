import { AppRoot } from '../AppRoot';
import { useRouter } from '../../lib/router';
import { CartSheetContent } from './CartSheet';
import { useT } from '../../i18n';
import { pagePath } from '../../lib/asset';

export function CartView() {
  const { navigate } = useRouter();
  const { t } = useT();
  return <section className="container" style={{ paddingBlock: 28, maxWidth: 760 }}>
    <a href={pagePath("/")} className="btn btn-ghost btn-sm">{t('cartPage.continueShopping')}</a>
    <h1 className="mt-2">{t('cartPage.title')}</h1>
    <div className="card mt-2"><CartSheetContent close={() => navigate('/')} /></div>
  </section>;
}

export function CartPage() {
  return <AppRoot activeKey="shop"><CartView /></AppRoot>;
}
