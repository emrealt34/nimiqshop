import { AppRoot } from '../AppRoot';
import { HomePage } from './HomePage';

export function ShopPage() {
  return (
    <AppRoot activeKey="shop">
      <HomePage />
    </AppRoot>
  );
}
