# Frontend Duplikasyon / Konsolidasyon Raporu — DURUM

Kapsamlı tarama (tüm `frontend/js` — bundle hariç) sonucu. Tüm maddeler uygulandı.
İşaretler: ✅ tamam · 🔒 bilinçli ayrı (dokunulmadı)

## A. YÜKSEK DEĞER — GERÇEK KOPYA KOD

### ✅ A1. `quoteStages(q)` (orders.js) + `buildQuoteStages(q)` (order.js) — %95 aynı
→ `frontend/js/order-track.js`: tek `quoteStages(q)` 4 aşamayı **yalnızca id/status/timestamp**
  ile kurar; metin taşımaz. orders.js + order.js ortak kullanıyor, yerel kopyalar silindi.

### ✅ A2. `order.js terminal()` + `orders.js terminalStatus()/isDelivered()/isIssue()`
→ `order-track.js`: `isTerminalStatus / isDeliveredStatus / isIssueStatus` tek kaynak.

### ✅ A3. Chat satırı render'ı — support.js vs order.js (`chatMessageRow`)
→ `ui.js` içinde `chatMessageRow(m, myAddress)` ortak; order.js **ve** support.js kullanıyor
  (support.js'in satır içi kopyası + `identiconImg` importu kaldırıldı).

### ✅ A4. `home.brandToProduct` + `home.normalizeBrandsResponse` vs product.js/catalog-meta.js
→ `catalog.js`: `normalizeBrand(brand, country)` (markdown temizliği, logo regex, kind→type),
  `flattenBrands(data, country)` (family|country dedup, stok tercihi), `mapKind`, `extractLogo`.
  home.js (grid) ve product.js (detay) ortak kullanıyor.

### ✅ A5. `home.productThumb` + `order.orderThumb` + `activity.feedThumb`
→ `catalog.js`: `productThumb(p)` (img + error→`brandIconImg` fallback) — kartlar/grid için.
  `resolveMetaLogo(tile, family, country, {match})` — order hero + activity feed'in ortak
  brandMetaFor-çözümü (order.js `orderThumb` ve activity.js `feedThumb` sadeleşti).

### ✅ A6. `ui.STAGE_COPY` + quoteStages içindeki stage metinleri — çift kaynak
→ `STAGE_COPY` (ui.js) tek İngilizce metin kaynağı; `quoteStages` metinsiz aşama üretir.
  Backend stage'lerinin kendi title/desc'ı görsel çıktıyı değiştireceği için korunmadı (bkz. Errors).

### ✅ A7. `home.js` ölü kod temizliği
→ `nimUsdRate/btcUsdRate` ölü yazma silindi (kartlar NIM tahmini göstermiyordu).
→ **Ek:** `payWithNimFlow` (297 satır) hiçbir yerden çağrılmıyordu — home'daki ürün kartları
  doğrudan `/product` sayfasına linkliyor (SPA navigasyon), satın alma orada yapılıyor. Ölü akış +
  getirdiği 3 import (`askSingleDelivery`, `buildOrderRequest`, `createQuoteStep`/`renderQuotePayment`)
  ve `closeSheet` kaldırıldı. Home artık yalnızca katalog/arama render ediyor.

## B. ÖLÜ KOD — SİLİNDİ

| Modül | Sembol | Durum |
|---|---|---|
| ui.js | `countdownNode`, `sheetStepIndicator`, `withLoading`, `relativeTimeNode` | ✅ silindi |
| miniapp.js | `buildOpenPath`, `buildAndroidIntentUrl`, `buildIosCustomSchemeUrl`, `openInNimiqPayOfficial`, `runtimeLabel`, `nimiqPayLanguage`, `getEthereumProvider` | ✅ silindi (yalnız `inNimiqPay`, `getNimiqProvider`, `openInNimiqPay`, `detectMobilePlatform`, URL sabitleri kaldı) |
| util.js | `sleep` | ✅ silindi |
| ui.js | `nimIconImg` alias | ✅ silindi, çağrılar `nimIcon`'a çevrildi |
| identicon.js | `identiconUrl`, `canonicalIdenticonInput` | 🔒 modül içinde kullanılıyor (identiconImg) — export kaldırılmadı |

Ayrıca: pages'lerdeki kullanılmayan importlar temizlendi (home `openSheet`, order `fmtNIM`,
orders `fmtNIM/timeAgo`, product `nimAmountNode`, activity `copyButton`, profile `fmtUSD/copyText/kv`).

### B2. Export yüzeyi temizliği (son tarama)
- **Gerçekten ölü fonksiyonlar (hiçbir yerde çağrılmıyor → silindi):** api.js
  `getFamily/listBrands/getRatingSummary/testBuy/health`, session.js `getToken`,
  util.js `frag`, validate.js `isValidE164`, order-track.js `QUOTE_STAGE_IDS`.
- **Gereksiz `export` (yalnızca modül içinde kullanılıyor → lokal yapıldı):**
  cart.js `clearCart/setQty`, catalog.js `brandIconImg/normalizeBrand`,
  countries.js `COUNTRIES/POPULAR`, delivery.js `askEmailStep/askTopUpPhone/askTopUpStep/collectPhoneValue`,
  hub.js `getHub/loadHubApi`, icons.js `LUCIDE_NAMES`, identicon.js `identiconUrl`,
  nim.js `NIM_LOGO/nimAmountFor`, ui.js `KIND_META/qrSvgNode/starSVG`, util.js `$$`.

## C. BİRLEŞTİRİLEN YARDIMCILAR

### ✅ C1. `activity.fmtDuration` + `orders.durationNode` + `profile.fmtCountdown`
→ `util.js`: `fmtDuration(sec)` (activity + orders list "Total Xh Ym" için) ve `fmtCountdown(ms)`
  (profile reset countdown) tek yerden. Üç sayfa da import ediyor; yerel kopyalar silindi.

### ✅ C2. `cart.sessionMarket/sessionFX` — api.js'teki anahtar adlarının kopyası
→ `api.js`: `cachedFX()` export edildi (anahtar adları tek yerde). cart.js `cachedNimRate/cachedFX`
  kullanıyor; yerel sessionStorage okuyucuları sadeleşti.

### ✅ C3. `cleanFamilyName` / `parseCurrencyValue` / `quoteFaceValue` — tek util ✓ (önceki tur)

### ➕ Bonus: `order.js` `ratingSection` + `quoteRatingCard` — %90 aynı iki kart
→ Tek `ratingCard({ status, rating, rate })`; order (`rateOrder`) ve quote (`rateQuote`) çağrıları
  parametreyle. `giftChannelLabel(ch)` da iki yerdeki inline map'i tekilleştirdi.

### ➕ Bonus 2: login-lock kartı + Transactions kartı (son tarama)
- `orders/order/profile` üçü de aynı "Connect wallet" kilit kartını kopyalıyordu →
  `ui.js lockedSignInCard({ title, text, onConnect, lg, iconSize })`.
- order.js + track.js aynı `Transactions (public)` kartını kuruyordu →
  `ui.js kvCard(title, rows)`.

## D. BİLİNÇLİ OLARAK AYRI (🔒 DOKUNULMADI)
- `quote-pay.renderQuotePayment` vs `order.js` "Pay now" kartı — `lightningPayBlock` zaten ortak.
- `home.sortProducts/productPriceText` — home'a özgü.
- `support.js` büyük form vs `order.js` satır içi form — aynı API, farklı UI.
- `cart.js` satır fiyatı — sepetin kendi veri modeli.
- `order.orderThumb` vs `catalog.productThumb` — farklı veri kaynağı (payload img vs logo_url);
  ortak kısım `resolveMetaLogo`'ya çekildi.

## E. BACKEND TESPİTLERİ (raporlandı — DOKUNULMADI)
Go derleyicisi bu ortamda yok; backend değişikliği derlenemez/doğrulanamaz, bu yüzden
yapılmadı. İstendiğinde derleyicili ortamda yapılabilir:
- **`order_handlers.go` `GetOrder` == `RefreshOrder`** — birebir aynı gövde (yalnızca yorum farkı).
  `RefreshOrder` kaldırılıp rota `GetOrder`'a yönlenebilir veya tek fonksiyon kalır.
- **`admin_api_handlers.go` `AdminGetOrderDetail` == `AdminRefundOrder`** — %100 aynı gövde;
  iade fonksiyonu yalnızca detay döndürüyor gibi görünüyor (iade mantığı başka yerde mi?).
- **db katmanı `List*` deseni** — `ListOrders/ListFeedOrders/ListOrdersByStatus/ListQuotesForUser/
  ListFeedQuotes/ListSupportTicketsForUser` aynı "user_id'ye göre çek + scan + JSON" şablonunu
  kopyalıyor (%-85-96 benzer); ortak `listUserRows` yardımcısına alınabilir.
- **`AdminAddSupportMessage` vs `AddSupportMessage`** (%87) ve **`LoadMeta` vs `LoadCatalogSnapshot`**
  (%96) benzer kopyalar.

## Doğrulama
- `node --check` tüm kaynak dosyalar ✅
- `delivery-harness.mjs` (jsdom): 14/14 ✅ — top-up'ta email hiçbir yerde yok; hediye email notu
  yalnızca alıcı emaili gerektiğinde; gift bayraksız hiçbir gift alanı sızmıyor; kart/top-up karma
  sepetinde top-up yalnızca telefon taşıyor.
- `node build.mjs` → 9 sayfa, 29 kaynak modül; eksik/atıl chunk yok; HTML entry eşleşmesi tam ✅
- Named/default export çözümlemesi (tüm modüller arası) ✅
- jsdom modül yükleme smoke testi (18 ortak modül) ✅
