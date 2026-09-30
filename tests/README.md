# Test paketi

GitHub Actions **Tests** iş akışı hepsini paralel çalıştırır — ama her push'ta
değil: hafta içi her gece (`0 0 * * 1-5` UTC), elle (`workflow_dispatch`) ve
`e2e` etiketi taşıyan PR'larda. Normal push'ta yalnızca hızlı **CI** iş akışı
(tip kontrolü, çeviri/API sözleşmesi, lint, build, Go test) koşar; GitHub
Pages yayını da CI yeşilse aynı iş akışından yapılır. Tests'teki herhangi bir
hata o çalıştırmayı kırmızı yapar ve `main` üzerinde görünür.

| İş | Ne kontrol eder |
| --- | --- |
| Static checks | TypeScript, çeviriler (eksik/fazla anahtar, `{placeholder}` ve etiket uyumu, çevrilmemiş metin), frontend ↔ backend API rotası eşleşmesi |
| Responsive · desktop / tablet / phone | Her sayfa × 6 dil × her ekran boyutu: yatay kaydırma, taşan/kesilen/üst üste binen yazı, 10px altı font, 24px altı dokunma alanı, bozuk görsel, sabit alt çubuğun altında kalan içerik, gerçek Nimiq identicon, ekrana düşen ham çeviri anahtarı. Menüler, modallar, açılır listeler, sekmeler tek tek açılıp aynı kontroller yapılır. |
| Functional · chromium | Yönlendirme, kırık link, çökme (her sayfa × her dil, her butona basılır), yavaş/bozuk API dayanıklılığı, header, etki kartı + PNG, paylaşım penceresi, identicon, dil değiştirme |
| Smoke · firefox / webkit / mobile-safari | `@smoke` etiketli testler diğer tarayıcı motorlarında |

Ekran boyutları: **desktop** 1280·1440·1920, **tablet** 768·820·1024, **phone** 320·360·390·430.

## Ekran görüntüleri

Actions → ilgili çalıştırma → **Artifacts → `responsive-screenshots`** indir.
Klasör yapısı:

```text
screenshots/
  index.html                 ← hepsini başlıklarla gösteren galeri
  desktop/<sayfa>/<dil>/1280x800.jpg, … , issues.txt
  tablet/…
  phone/…
```

`issues.txt` o sayfada bulunan her sorunu boyutuyla birlikte yazar
(ör. `[text-truncated] <a> "BESTELLUNGEN" — needs 98px, has 88px`).
Açılan her menü/modal için ayrı görüntü de vardır.
Hata detayları ve trace'ler: **`playwright-report`** artifact'ı.

## Yerelde çalıştırma

```bash
npm ci
npx playwright install --with-deps
npm run test:e2e:build                 # GitHub Pages düzeninde build (/nimiqshop/)
npm run test:e2e:quick                 # hepsi
npx playwright test --project=phone    # sadece telefon taraması
SCAN_EXPLORE=0 npx playwright test --project=phone   # menüleri açmadan, hızlı
npx playwright test --project=phone --grep "cashback · de"
npm run check && npm run check:i18n:deep && npm run check:api   # statik kontroller
```

## Kasıtlı tasarımlar için istisnalar

Tarayıcı kasıtlı bir durumu hata saymasın diye elemana şu attribute'lar eklenebilir.
Seyrek kullanın, çünkü kodda görünürler ve gözden geçirilirler:

- `data-allow-truncate` → bilerek `…` ile kısaltılan metin (adres, hash)
- `data-allow-overflow` → kutusundan taşması normal olan eleman (kayan yazı vb.)

Bilerek çevrilmeyen metinler (marka adları vb.): `scripts/i18n-untranslated-allow.json`.

## Yeni sayfa / API rotası eklerken

- Yeni sayfa: `tests/e2e/support/data.ts` → `SCREENS` listesine ekleyin. Responsive, çökme ve link testleri otomatik kapsar.
- Yeni API çağrısı: backend'de rota yoksa `check:api` kırmızı olur. Testlerdeki sahte cevap `tests/e2e/support/fixtures.ts` → `payload()` içindedir; gerçek backend şemasıyla aynı tutun.
