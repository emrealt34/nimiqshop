import { AppRoot } from '../AppRoot';
import { HomePage } from './HomePage';

export function ShopPage({ initial }: { initial?: string }) {
  return (
    <AppRoot activeKey="shop" initial={initial}>
      <HomePage />
    </AppRoot>
  );
}
