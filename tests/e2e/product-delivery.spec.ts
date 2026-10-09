/**
 * A mobile family can contain both emailed PINs and direct phone top-ups.
 * Every delivery surface must follow the selected package's supplier channel,
 * not the family-wide category (Ay Yildiz regression, 2026-10-09).
 */
import { test, expect, open } from './support/fixtures';
import { path } from './support/data';

const packageOf = (id: string, deliveryType: string, value: number, label: string) => ({
  product_id: id,
  delivery_type: deliveryType,
  denomination: `${value} EUR`,
  localized_denomination: label,
  coin: 'BTC',
  coin_amount: '0.0002',
  face_value: { currency_code: 'EUR', amount: { type: 'fixed', price: String(value) } },
});

const mixedFamily = {
  family: 'Ay Yildiz',
  brand: 'Ay Yildiz Credits',
  kind: 'mobile_recharge',
  category: 'mobile_credits',
  country_code: 'DE',
  logo_url: 'https://logos.example.test/ay-yildiz.svg',
  bg_color: '#FFFFFF',
  is_out_of_stock: false,
  products: [
    packageOf('emailed-pin', 'by_email', 2.5, '€2,50'),
    packageOf('phone-topup', 'by_phone', 15, '€15'),
  ],
};

const COPY = {
  en: {
    email: 'Instant email delivery',
    phone: 'Instant phone top-up',
    code: 'Code sent by email',
    credit: 'Credit applied to your number',
    phoneDescription: 'phone number you enter',
    esim: 'Instant eSIM QR',
    esimEmail: 'Instant eSIM by email',
    qr: 'QR sent by email',
  },
  tr: {
    email: 'Anında e-posta teslimatı',
    phone: 'Anında telefon yüklemesi',
    code: 'Kod e-posta ile gönderilir',
    credit: 'Kontör numarana yüklenir',
    phoneDescription: 'girdiğin telefon numarasına',
    esim: 'Anında eSIM QR',
    esimEmail: 'E-posta ile anında eSIM',
    qr: 'QR e-posta ile gönderilir',
  },
};

for (const lang of ['en', 'tr'] as const) {
  test.describe(`product delivery copy (${lang})`, () => {
    test.use({
      lang,
      api: { authed: false },
      viewport: lang === 'tr' ? { width: 390, height: 844 } : { width: 1280, height: 800 },
    });
    const copy = COPY[lang];

    test('mixed family keeps hero, description and footer in sync when switching packages', async ({ page }) => {
      await page.route('**/api/catalog/products/**', (route) => route.fulfill({ json: mixedFamily }));
      await open(page, path('/product?id=Ay%20Yildiz&country=DE'));

      const hero = page.locator('.pd-info .chips-row .chip').first();
      const footer = page.locator('.howto-mini .chip').first();
      const target = page.locator('.howto-mini .chip').nth(1);
      const description = page.locator('.pd-info > p');

      // The first/cheapest package is an emailed PIN, not a phone credit.
      await expect(hero).toContainText(copy.email);
      await expect(footer).toContainText(copy.email);
      await expect(target).toContainText(copy.code);
      await expect(description).toContainText(copy.email);
      await expect(description).not.toContainText(copy.phoneDescription);

      await page.locator('.pd-pkg').filter({ hasText: '€15' }).click();
      await expect(hero).toContainText(copy.phone);
      await expect(footer).toContainText(copy.phone);
      await expect(target).toContainText(copy.credit);
      await expect(description).toContainText(copy.phoneDescription);
      await expect(description).not.toContainText(copy.email);

      // Switching back must not leave the phone wording stuck in the footer.
      await page.locator('.pd-pkg').filter({ hasText: '€2,50' }).click();
      await expect(hero).toContainText(copy.email);
      await expect(footer).toContainText(copy.email);
      await expect(target).toContainText(copy.code);
      await expect(description).toContainText(copy.email);
    });

    test('ordinary gift cards keep their email delivery wording', async ({ page }) => {
      await page.route('**/api/catalog/products/**', (route) => route.fulfill({
        json: { ...mixedFamily, family: 'Gift Card', kind: 'giftcard', category: 'e-commerce', products: [mixedFamily.products[0]] },
      }));
      await open(page, path('/product?id=Gift%20Card&country=DE'));
      await expect(page.locator('.pd-info .chips-row .chip').first()).toContainText(copy.email);
      await expect(page.locator('.howto-mini .chip').first()).toContainText(copy.email);
      await expect(page.locator('.howto-mini .chip').nth(1)).toContainText(copy.code);
    });

    test('emailed eSIMs keep their QR wording instead of becoming gift cards', async ({ page }) => {
      await page.route('**/api/catalog/products/**', (route) => route.fulfill({
        json: { ...mixedFamily, family: 'eSIM', category: 'e-sim', products: [mixedFamily.products[0]] },
      }));
      await open(page, path('/product?id=eSIM&country=DE'));
      await expect(page.locator('.pd-info .chips-row .chip').first()).toContainText(copy.esim);
      await expect(page.locator('.howto-mini .chip').first()).toContainText(copy.esimEmail);
      await expect(page.locator('.howto-mini .chip').nth(1)).toContainText(copy.qr);
    });
  });
}
