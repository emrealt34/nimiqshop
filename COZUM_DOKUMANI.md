# Rate-Limit / Log Spam / Ürünler Görünmüyor — TAM ÇÖZÜM DOKÜMANI

**Tarih:** 2026-08-26 · **Kapsam:** `cryptorefills supplier budget exhausted` spam'i, ürün kataloğunun kaybolması, supplier rate-limit'e karşı tam koruma

---

## 1. O logun gerçekte ne olduğu

```
tracker: poll order 2fa8c6aa-…: cryptorefills supplier budget exhausted; retry shortly
```

Bu mesaj **Cryptorefills'in değil, KENDİ kuyruğumuzun** hatasıydı (`ErrBudgetWait`).
Sıra şöyle işledi:

1. `WORKER_ORDER_POLL_SECS=4` ile tracker **her 4 saniyede** **her açık siparişi** tek tek poll ediyor.
2. 3 açık sipariş = dakikada ~45 poll. Tracker'ın aktör bütçesi (60/dk) ve endpoint penceresi
   dolunca kuyruk isteği **reddediyor** (`ErrBudgetWait`) — upstream'e hiç gitmeden.
3. Eski binary'de backoff YOKTU: reddedilen sipariş **4 saniye sonra tekrar** deneniyor,
   yine reddediliyor, yine loglanıyor… Sonsuz döngü. 7+ dakikada aynı 3 UUID için
   yüzlerce satır = gönderdiğin log.
4. Mesaj "supplier budget exhausted" dediği için herkes tedarikçiyi suçluyordu — o da düzeltildi.

## 2. Ürünlerin görünmemesinin asıl sebebi

`GetFamily` (ürün detay) handler'ı supplier'dan **boş** sonuç geldiğinde onu **1 saatliğine
gerçek veri gibi cache'liyordu**. Bir kere boş dönen glitch sonrası o ürünün sayfası
TTL boyunca 404 — restart gerekmeden düzelmez. Aynı risk markalar listesinde de vardı.
**Artık boş sonuç asla taze veri gibi cache'lenmiyor** (aşağıda katman katman).

---

## 3. Yapılan tüm düzeltmeler (katman katman)

### Katman 0 — Frontend (`frontend/js/api.js`)
- **Sayfa içi GET cache:** `/catalog/brands` 5dk, `/catalog/products` 2dk, `/catalog/price` 1dk,
  `/catalog/payment-vias` 10dk, `/market` 30sn, `/activity` 10sn, `/track` 5sn.
- **Aynı isteğin seri tekrarı tekilleşiyor:** aynı URL için uçuştaki istek varsa yeni fetch
  atılmaz, herkes aynı cevabı bekler (F5 spam'i = 0 network çağrısı).
- **Stale fallback:** network tamamen giderse son 10 dakikanın cache'i servis edilir — sayfa boş kalmaz.

### Katman 1 — Supplier istemcisi (`internal/cryptorefills/client.go`)
- **Tüm GET'ler mikro-cache + singleflight:** aynı anda gelen özdeş istekler (tracker tick +
  webhook + kullanıcının "yenile" tıklaması) **tek upstream çağrısına** iner.
  - `GetOrder` 3sn · `Brands` 3dk · `ProductsByCountry` 3dk · `Price` 45sn · `PaymentVias` 10dk
- **Hatalar asla cache'lenmez** (bir 429/timeout anahtarı zehirleyemez); lider başarısızsa
  takipçiler kendi bağlamlarıyla tekrar dener.
- Boş `ProductsByCountry` sonucu sadece **60sn** negatif cache.

### Katman 2 — Katalog cache'i (`internal/handlers/catalog_handlers.go`, `cache.go`)
4 katmanlı derin cache:

```
L1 taze RAM    → brands 6sa · family 1sa · price 10dk · vias 12sa (isteklerin %99'u biter)
L2 bayat RAM   → supplier hata verirse "hafif bayat" veri servis edilir (30 güne kadar)
L3 disk (BADGER) → her başarılı cevap kalıcı snapshot; restart sonrası ve uzun kesintide
                   mağaza yine de TAM DOLU açılır (snap:cat:* keyspace, 30 gün ömür)
L4 supplier    → anahtar başına taze TTL'de en fazla 1 çağrı; tüm eşzamanlı istekler
 TEK singleflight üzerinden geçer
```

- **Boş sonuç = `errEmptyCatalog`**: taze cache'e YAZILMAZ, L2/L3 fallback tetiklenir.
  Ürünlerin bir daha görünmemesi bug'ının birebir karşı önlemi.
- **Negatif cache sadece 30sn** (gerçekten olmayan family için 404 supplier'ı yormadan).
- `Cache-Control: public, max-age=…` header'ları → tarayıcı + Cloudflare de cache'e girer.
- `lookupFamilyMeta` artık hatayı 10dk değil **30sn** cache'ler (telefon-zorunluluğu kontrolü
  bir glitch sonrası 10dk boyunca bozuk kalmıyordu).

### Katman 3 — Tracker (`internal/settlement/track.go`) — asıl olay yeri
- **Adaptif poll takvimi** (her sipariş kendi hızında):
  | durum | hız |
  |---|---|
  | `awaiting_payment` 0-2dk | 15 sn |
  | `awaiting_payment` 2-5dk | 30 sn |
  | `awaiting_payment` 5-15dk | 60 sn |
  | `awaiting_payment` 15dk+ | 120 sn |
  | `payment_started` / `payment_received` | 10 sn |
  | `delivering` | 8 sn |
- **Tick başına en fazla 8 upstream poll** (`MaxPollsPerTick`) — 100 açık sipariş olsa bile.
- **Restart'ta stampede yok:** ilk poll, ID hash'iyle aralığa yayılır (stagger).
- **Cooldown atlama:** scheduler "sıradaki poll 2sn+'ye konumlanır" diyor ise **tüm pass atlanır**
  (`OrderPollWait()`), dakikada bir log. Eskiden her sipariş için her tick bir reddetme logu vardı.
- **Per-sipariş üstel backoff + %20 jitter:** 30sn → 90sn → 4.5dk → 13.5dk → 30dk (tavan).
- **Hata sınıflandırma:**
  - *Geçici* (429, yerel bütçe, timeout, ağ, 5xx) → backoff, **ASLA manual_review**.
    "5 hata → manual_review" saçmalığı KALDIRILDI.
  - *Kalıcı* (supplier 400/401/403/404 — sipariş yok/anahtar geçersiz) → tek sefer manual_review
    (sonsuz retry'ın düzeltmeyeceği tek durum bu).
- **Global devre kesici:** 60sn'de 8 geçici hata → tüm poll 2dk duraklar (ardışık trips'te
  4dk, 8dk… 15dk tavan). Tek success streak'i sıfırlar. Tek SEFER log.
- **Log dedup:** aynı sipariş + aynı hata sınıfı → en fazla 10dk'da bir satır.
- **Yanıltıcı mesaj düzeltildi:** `ErrBudgetWait` artık "yerel kuyruk bütçemiz doldu
  (supplier sorun değil)" diyor.

### Katman 4 — Kuyruk (`internal/cryptorefills/queue.go`)
- `QueueStats.Throttled`: hangi endpoint'ler soğumada → `/api/health` artık
  `{"ok":true,"cr_queue":{"queued":0,"actors":1,"throttled":["GET /v5/orders/{id}"]}}` diye raporlar.
- `endpointWait()`: tracker'ın cooldown'u sorması için.

### Katman 5 — Boot & bakım (`cmd/server/main.go`)
- Açılışta **disk snapshot'ları RAM'e preload** edilir → supplier ölü olsa bile ilk istek dolu vitrinle karşılanır.
- Cachewarm artık **sıralı** (4 ülke paralel değil, 2sn arayla), hata olursa üstel retry (1dk→30dk).

---

## 4. Neden artık rate-limit'e takılmak imkânsız (üst üste 7 duvar)

Bir isteğin supplier'a ulaşabilmesi için şunların TAMAMINI geçmesi gerekir:

1. Tarayıcı cache'i (Cache-Control)
2. Frontend sayfa içi cache + in-flight tekilleştirme
3. Handler L1 taze cache (6sa'ya kadar)
4. Client mikro-cache + singleflight (özdeş istek = 1 çağrı)
5. Endpoint bütçeleri (brands 60/dk, orders 120/dk …) + aktör başına adil kota
6. Tracker: adaptif takvim + tick başına 8 poll + backoff + breaker + cooldown atlama
7. Hâlâ kısırlıysa: 429 gelirse Retry-After'a tam saygı + 30sn-30dk üstel geri çekilme

Yani normalde (1)-(4) tüketir; supplier'a saniyede en fazla birkaç, felaket senaryosunda bile
tick başına 8 istek gider ve 429 görüldüğü an sistem **kendini kapatarak** bekler.

## 5. Doğrulama (hepsi bu zip'teki kodla koşuldu)

- `go build ./...` ✅ · `go vet ./...` ✅ (önceki zip **derlenmiyordu**: `track.go:82 declared and not used: ts`)
- Tüm birim testler ✅ + **yeni regresyon testleri:**
  - `TestTrackerStopsPollingDuringSupplier429` — kazanın birebir simülasyonu: 3 sipariş +
    tedarikçi 429 → cooldown boyunca upstream çağrı **sıfır** (yalnız 1 "keşif" çağrısına izin),
    tüm siparişler backoff'ta, **kimse manual_review'a düşmüyor**.
  - `TestBreakerOpensAndResets`, `TestMarkDueStaggersFirstSight`, `TestPollIntervalForAdaptive`,
    `TestClassifyPollError`, `TestFlightGroupCollapses`, `TestGetCacheCollapsesConcurrentFetches`,
    `TestSnapshotRoundtrip` …
- **crashtest süiti (C01–C09) tamamen geçti** (garbage storm, firehose, kuyruk tükenmesi,
  SIGKILL, webhook flood, DB bozulması, restart fırtınası, happy path, beneficiary kuralları).
  - Not: C04'ün "tüm paket içinde" patlaması **önceden var olan** bir süit tasarımı hatasıydı —
    orijinal zipte de aynen patlıyordu (kanıtlandı); C03'ün 10dk'lık POST bütçesini bitirmesi
    nedeniyle C04 başında server restart edilecek şekilde düzeltildi.

### 5a. CANLI UÇTAN UCA TEST RAPORU (gerçek binary + mock supplier + 429 fırtınası proxy'si)

Senaryo: gerçek satın alma (JWT ile `POST /api/quotes` → BOLT11 fatura), ardından supplier
**TÜM isteklere 429** demeye başlıyor, sipariş supplier tarafında ödenip teslim ediliyor,
sonra fırtına kalkıyor:

```
14:36:14 tracker: order ord_5ac85793eb17 poll paused 27s (attempt 1, supplier 429)
14:36:16 tracker: order polling paused 10s (order endpoint cooling down; supplier budget recovering)
14:36:42 tracker: quote cd6e8df4-... fulfilled (supplier Done)
```

- Fırtına penceresinde upstream order-poll sayacı: **1 → 1** (sıfır ekstra çağrı; eski sistem
  4 saniyede bir dalardı)
- Bütün yaşam döngüsünde supplier'a giden order-poll: **toplam 2** (1 keşif + 1 toparlanma)
- Fırtına kalktıktan 28 saniye sonra sipariş otomatik `fulfilled`, 4 aşamanın hepsi completed
- Ayrıca: supplier tamamen öldürüldükten sonra server restart → vitrin **disk snapshot'tan
  dolu** açıldı (`preloaded 1 disk snapshot(s) at boot`); tekrar istekler **0.27ms** (L1 cache)

### 5b. Bu sürümde ek düzeltilen 2 kusur
1. Client negatif-cache bug'ı: boş ürün/marka sonucu kısa TTL ile yazılıp hemen ardından uzun
   TTL ile EZİLİYORDU → `doNeg` ile boş sonuç gerçekten 60sn'de siliniyor.
2. Cachewarm retry timer'ı başarılı geçişte de gereksiz tur atıyordu → retry artık yalnız
   hatada, üstel (1dk→30dk) devreye giriyor.

## 6. Deploy notları

- `.env` DEĞİŞİKLİĞİ GEREKMEZ. `WORKER_ORDER_POLL_SECS=4` kalabilir — artık "sipariş başına
  hız" değil, sadece tick inceliği. TTL'ler kod içinde sabit (kasıtlı: knob çokluğu = hata çokluğu).
- Yeni log satırları: `order endpoint cooling down`, `circuit breaker`, `disk snapshot`,
  `cachewarm: refreshed brands(TR)`. Spam satırları: yok.
- Sağlık kontrolü: `GET /api/health` → `cr_queue.throttled` boşken her şey normâl.

## 7. UI/UX düzeltmeleri (bu sürüm)

1. **"1 sipariş açıldı 3 tane oldu" — kökten çözüldü.** Sebep: her tıklama YENİ idempotency
   key taşıyor, key bazlı koruma hiç ateşlemiyordu. İki katman eklendi:
   - Backend: aynı alıcı, BİREBİR aynı sepet (ürün+ülke+denomination+değer+adet+e-posta+telefon),
     son 60 saniye içinde ve sipariş hâlâ canlıysa (ödenmemiş) → İLK quote geri döner,
     ikinci supplier siparişi AÇILMAZ (`duplicate-purchase guard`, log'a da yazar).
     Bilinçli yeniden alım (ilk sipariş fulfilled/failed/expired olduktan sonra) serbest.
   - Frontend: aynı sepet için uçuştaki istek tekilleşir + 90 sn içinde taze quote cache'ten döner.
2. **"Product not found" fiilen imkânsız.** `getFamily` boş sonuç geldiğinde artık hemen 404
   vermiyor: sırayla bayat RAM katmanı → disk snapshot'a bakıp SON BİLİNEN İYİ ürün listesini
   servis ediyor. Hiç veri olmadıysa (ürün gerçekten hiç var olmamışsa) 404 hâlâ doğru davranış.
3. **Avatarlar cüzdanla aynı:** Nimiq avatarı FRIENDLY adres formundan (4'lü gruplar, boşluklu)
   hash'lenir; kod boşlukları silip FARKLI hash üretiyordu → cüzdanla uyuşmayan avatar. Artık
   canonical form kullanılıyor + açılışta yanlış fallback gradyanı yanıp sönmesi kaldırıldı.
4. **Daily limits "Resets in now":** kullanım yokken reset zamanı `time.Now()` hesaplanıyordu.
   Artık tam 24 saat ileri; geri sayım sunucu saatine göre (cihaz saati yanlışsa telafi edilir)
   İngilizce "Resets in 3h 12m" biçiminde.
5. **Ana sayfa metinleri:** "FRESH ⚡ DELIVERY" → "FAST ⚡ DELIVERY"; "Nimiq Pay → BTC Lightning,
   settled in ~30 s" → "Nimiq Pay: NIM → BTC Lightning, settled in ~5 s"; "Codes land on your
   orders page instantly" → "Codes land on your email instantly".
6. **Sağ scrollbar:** kategori (everything/giftcards/topups) sekmelerinde beliren ana sayfa
   scrollbar'ı görsel olarak gizlendi (kaydırma tekerlek/dokunma ile aynen çalışır).
7. **Sessiz sayfa polling'i:** SPA geçişlerinde eski sayfanın arka plan poller'ları sonsuza kadar
   çalışıyordu (Activity sayfasından çıkınca bile /api/activity + /api/presence 15/30 sn'de bir,
   Support ticket'ları, Orders…). Yeni `pageInterval()` mekanizması: sayfa değişince o sayfanın
   TÜM zamanlayıcıları kapanır — arka planda hiçbir gizli istek kalmaz.

## 8. "Bazı ülkelerde satın alamıyorum" — kök neden + 244 ülke süpürmesi

**Kök neden (2 bug):**
1. Frontend parser `parseFloat` kullanıyordu: "150.000 IDR" → **150**, "1.000.000 VND" → **1**,
   "1 000 AED" → **0** okuyordu. Yüksek değerli para birimlerinin (IDR, VND, IQD, LBP, COP,
   UZS, KHR…) ürünleri `faceValue < 10000` filtresine takılıp **komple düşüyordu** → sepet
   boşalıyor → checkout `denomination:''` gönderiyor → "denomination is required" hatası,
   fiyat yok, al tuşu yok.
2. Backend `resolveFaceValue` da aynı 10.000 tavanı + aynı kötü parse'i kullanıyordu.

**Çözüm:** Tek doğru parser iki tarafta da (Go + JS, birebir ikiz):
binlik ayraçlar (nokta/virgül/boşluk), ondalık virgül ("25,50"), karışık ("1.234,56"),
semboller (₺€£¥₩₹), yapışık/başta/sonda kod ("TRY300"/"100 USD"/"120 TL"), unicode
rakamlar (٥٠٠) — ve numarası hiç olmayan etiketler ("Java & Bedrock Ed") artık
**label-only** satın alınıyor (supplier etiketten fiyatlıyor), hata DÖNMÜYOR.
Range markalarda checkout artık daima `denomination:"range"` + doğru product_value.

**Kanıt — canlı süpürme (gerçek server + mock, crashtest YOK):**
- 244 ISO ülke kodu × (markalar → ürünler → fiyat): **244/244 başarılı**, 0 hata, 0 "not found"
- 13 para-formatı ailesinden canlı quote (TRY300, £10, "25,50 EUR", "150.000 IDR",
  "1.000.000 VND", "5.000 IQD", "1 000 AED", "₩5,000", "₹500", "10 000 RUB", "٥٠٠ SAR",
  "R$ 50", "2,500 PKR"): **13/13 → 201 Created**, sıfır "denomination is required"
- Birim testler: Go 40+ durum, JS 29 durum — hepsi yeşil

## 9. GERÇEK FRONTEND akışıyla test (jsdom içinde birebir product.js)

Bu kez API'yi değil, **tarayıcıda çalışan gerçek frontend modüllerini** (product.js + shell +
api + session — hiçbiri değiştirilmeden) jsdom içinde çalıştırıp gerçek kullanıcı gibi gezildi:
ürün render → denomination seçimi → "Buy with NIM" → e-posta → "Continue" → gerçek
`POST /api/quotes` → **ödeme ekranında fiyat + Lightning faturası**.

Mock'a `country_currencies` fault'u eklendi: her ülke ARTIK GERÇEK yerel formatında katalog
veriyor ("10.000 IDR", "₩5,000", "25,50 EUR", "TRY300", "٥٠٠ SAR"…) + numarasız etiketli
label-only ürün ("Java & Bedrock Ed") eklendi.

**Matris: 23/23 PASS** — 17 ülke range marka (TR US GB DE FR ID VN IQ AE KR IN RU SA BR HU CO
PK) + 4 ülke label-only + 2 ülke fixed. Sıfır "denomination is required", sıfır ölü uç.

**Bu turda yakalanan + düzeltilen 3 GERÇEK bug:**

1. **`db/quotes.go` ölü invariant — KRİTİK:** `ProductUSD <= 0 → ErrConflict`. Etiketi
   numarasız HER fixed ürün ("Java & Bedrock Ed" — kodda gerçek ürün örneği olarak geçiyor)
   quote oluşturma aşamasında **sessizce 409 "idempotency key already used"** yiyordu.
   Yani o ürünler HİÇBİR ZMANY satılamıyordu. Artık ProductUSD=0 (label-only) geçerli;
   negatif hâlâ reddedilir. Regresyon testi eklendi.
2. **product.js çift parser:** util'den import edilen yeni parser ile eski local bozuk parser
   çakışıyordu (SyntaxError) → local kopya silindi.
3. **Ölü durum UI:** ürünün hiç SKU'su yoksa eskiden yanıltıcı "Live denominations at
   checkout" mesajı çıkıyor ve al tuşu hataya gidiyordu → artık "bu ülkede şu an
   satılamıyor" mesajı + al/sepete-ekle butonları düzgünce devre dışı.
   Ek olarak `face_value`'nun {"type":"fixed","value":...} şekli de parse ediliyor ve
   hiç etiket yoksa ürün id'si son çare etiket olarak kullanılıyor.

## 10. NIM gösterimi, adetli fiyat, admin fiyat sınırı (USD → her ülke birimi)

1. **Ürün sayfasında artık BTC YOK, NIM var:** chip'lerdeki tahminler NIM cinsinden
   ("26.5K NIM"); oranlar gelmezse "NIM at checkout" — ham BTC rakamı vitrine çıkmaz.
2. **Al butonunda canlı toplam fiyat:** "Buy with NIM ≈ 26.5K NIM"; adedi 2 yapınca anında
   "Buy 2 × — ≈ 52.9K NIM". Ödeme ekranında da "2 kod teslim edilecek — tutar hepsini kapsar,
   tanesi ≈ X NIM" satırı.
3. **Admin fiyat sınırı artık her ülkede DOĞRU çalışıyor:** Eski kod "150.000 IDR > 20"
   diye ham karşılaştırıp yüksek-değerli ülkelerin tamamını gizliyordu. Artık:
   - Yeni `/api/market/fx` uç noktası küratörlü USD-kuru tablosunu servis eder
     (160+ para birimi) — backend filtresi VE frontend tahmini AYNI kuru kullanır.
   - Admin paneline "Catalog rules — price cap & visibility" kartı EKLENDİ: USD cap
     (0=kapalı), gizli markalar, yasaklı kategori/kind, gizli/izinli ülkeler,
     out-of-stock politikası — tek karttan, audit-loglu kayıt.
   - "20" yazarsa: "150.000 IDR" (≈$10) GÖRÜNÜR, "500.000 IDR" (≈$33) GİZLENİR;
     range slider'lar USD karşılığı tavana göre kırpılır (adım adım yuvarlanır),
     over-cap fixed SKU'lar tarayıcıya hiç gönderilmez.
   - Birim testler: ToUSD tablosu + cap filtresi (cheap/pricey IDR, fixed, brand min).

## 11. Konsol hataları + scroll sıçraması (bu sürüm)

1. **`Uncaught SyntaxError: Identifier 'parseCurrencyValue' has already been declared`** —
   zip 15'teki çift parser tanımıydı; zip 16'dan beri koddan tamamen temiz
   (grep ile doğrulandı: 0 tanım). Bu zip'i deploy edince kaybolur.
2. **`TypeError: Cannot read properties of null (reading 'firstChild')` (home → renderGrid)**
   — sayfa değiştikten SONRA gelen async callback'ler (geo lookup, ülke/sıralama)
   ölü DOM'a render etmeye çalışıyordu. `clear()`/`replaceChildren()` artık null-güvenli;
   `renderGrid`/`loadCatalogs`/geo-callback koruma ile no-op.
3. **Orders sayfasında iki kopya oluşup F5'te biri kaybolması** — SPA gezinme kilidi
   modül import'u bitmeden bırakılıyordu; hızlı çift tıklamada iki modül art arda render
   ediyor, sonrakiler üst üste binuyordu. Gezinme kilidi artık import TAMAMLANANA kadar
   tutuluyor → yarış imkânsız.
4. **CSP `img-src` ihlalleri (1444 adet)** — tedarikçi logoları değişik CDN'lerden geldiği
   için `https://*.cryptorefills.com` kısıtı blokluyordu. `img-src 'self' data: blob: https:`
   yapıldı (_headers + tüm HTML meta'ları) — logolar artık açılıyor.
5. **Ülke/sıralama değişiminde sıçrama + sağ scrollbar** — `scrollbar-gutter: stable`
   (yatay kayma yok) + `#gridWrap { min-height: 62vh }` (grid boşalınca dikey sıçrama yok).

## 12. Ürün sayfası açılış hızı

Eski akış render'dan ÖNCE 3 isteği SIRAYLA bekliyordu: ürün → NIM oranı (oracle soğuksa
12 sn'ye kadar) → FX tablosu. Sayfa "Loading product…" da donuyordu.

Yeni akış:
- **Sadece katalog isteği render'ı bloklar** — ürün anında çizilir.
- NIM oranı + FX tablosu **paralel ve arka planda** yüklenir; gelince çipler ve al-butonu
  kendini günceller ("NIM at checkout" → "≈ 26.5K NIM" gibi).
- Ana sayfa kartlarında **hover/focus/touch prefetch**: linke gelince arka planda ürün
  verisi cache'e düşer — tıklayan ziyaretçi ürün sayfasını anında açar.

Ölçüm: tam frontend render (jsdom) **~222 ms**; backend products soğuk ~1 ms (mock),
sıcak <1 ms; FX <1 ms. Artık tek sınır gerçek ağ gecikmesi.

## 13. Animasyonlar, cart para birimi, Lightning fallback

1. **Animasyonlar KAPALI:** sayfa geçişlerinde view-transition ve page-enter/fade-in
   animasyonları kaldırıldı — sayfalar "tak" diye anında açılıyor, ürün sayfasında
   scrollbar flaşı da yok.
2. **Cart para birimi:** "250 TRY" ürün "$250.00" yazıyordu. Artık her ürün KENDİ
   para birimiyle ("₺250") görünüyor; toplam ise dürüst USD karşılığı + NIM tahmini
   ("Total ≈ $12.40 · ≈ 35K NIM") — backend FX tablosuyla hesaplanıyor.
3. **Checkout hızlandırıldı:** çok ürünlü sepetlerde quote'lar ARTIK PARALEL oluşturuluyor
   ("Preparing 3 orders…" + ilerleme), ödeme adımları sırayla aynen güvenli.
4. **`lightning:` handler yoksa boşa düşme yok:** ödeme ekranlarında her zaman
   "Copy Lightning invoice" butonu + "wallet açılmadıysa faturayı yapıştır" notu var —
   konsol hatası artık kullanıcıyı yol kaybettirmiyor.
5. **"Product not found" ölü sonu:** admin sınırı/ülke kuralı/stok nedeniyle görünmeyen
   ürünler artık dostane "Not available right now" kartı + "Browse the shop" ve
   "Try again" butonları gösteriyor. Client negatif-cache 60s → 30s.

## 14. NIM fiyatı: hep sıcak, asla bekleme

`/api/market/nim-rate` artık **asla oracle'ı beklemez**:
- Boot'ta hemen + her 60 saniyede bir arka plan refresher NIM/USD + BTC/USD'yi
  çeker ve bellek snapshot'ına yazar (3 kaynaklı medyan, yayılma korumalı).
- Kullanıcı isteği snapshot'tan **anında** döner (ölçüm: ~0.3 ms).
- Oracle geçeose eski snapshot korunur (bayat > boş); taze boot'ta (ilk ~1 sn)
  503 "warming up" + arka planda anında tazeleme — kullanıcı 2 sn içinde sıcak
  fiyata kavuşur.
- Cevap artık `age_seconds` da içerir (kaç saniye önceki fiyat).

## 15. Ödeme geri sayımı + support chat düzeni + draft koruması

1. **Ödeme ekranında geri sayım:** ürün sayfası, ana sayfa ve sepet ödeme
   ekranlarının hepsinde "⏳ Time left to pay: 29m 12s" aynı saniyede işleyen
   sayaç var (fatura 30 dk geçerli). Süre bitince ödeme linki pasifleşir,
   sepet akışı "Payment window expired" ile temizce durur.
2. **Ticket draft'ı ASLA kaybolmaz:** order sayfasındaki 12 sn'lik otomatik
   yenileme artık (a) kullanıcı support kartında yazıyorken, (b) kaydedilmemiş
   draft varken ATLANIR. Ayrıca konu/mesaj/cevap taslakları order-id bazlı
   saklanır ve her yeniden çizimde geri yüklenir — ticket yazarken refresh
   gelse bile yazı yerinde kalır.
3. **Chat avatar düzeni:** destek sohbetinde ALICININ mesajları SAĞDA kendi
   Nimiq identiconuyla, SUPPORT mesajları SOLDA headset avatarıyla
   (chat-row/bubble-col/bubble tasarımı CSS'te zaten vardı, kod sonunda
   bağlandı — support sayfasında da aynı düzen).

## 16. Range kartı tasarım düzeltmeleri

- **Kart artık alanının TAMAMINI kaplıyor** (`grid-column: 1 / -1`): eski dar
  kutu grid hücresine sıkışıyordu.
- **Taşma bitti:** min/max etiketleri kenarlarda sabit, canlı NIM tahmini
  ortada esnek — küçülünce kıvrılır, asla üstüne yazmaz.
- **Miktarlar ARTIK DOLAR DEĞİL:** "₺10 - ₺100" gibi ürünün KENDİ para
  birimiyle (fmtMoney + aralık currency'si).
- Mock'un ülke para kodu üretimi düzeltildi ("TRY1" yerine "TRY") —
  frontend tahminleri artık doğru kurla hesaplıyor (10 TRY ≈ 768 NIM).
- **Chat'e gönderen adı eklendi:** her baloncuğun üstünde "🛡️ Support" /
  "🟠 You" — kim ne yazdı + avatar tarafı net.

## 17. USD taraması + buildbar scroll düzeltmesi

- **Tüm uygulama tarandı:** vitrin akışında (ana sayfa / ürün / sepet) ARTIK HİÇ
  "$" gösterimi yok — her şey ürünün/dükkanın kendi para biriminde. Kalan tek
  fmtUSD kullanımları GEÇMİŞ siparişlerin kayıtlı USD değerleri (gerçek kayıt)
  ve hepsinde $0.00 yerine "—" guard'ı eklendi (label-only siparişler).
- **Sepet toplamı NIM önce:** "Total ≈ 35K NIM (≈ $12.40)".
- **Buildbar (hash doğrulama çubuğu) artık asla scroll alanı oluşturmaz:**
  overflow-x gizli, içerik sariniyor, dar ekranda alt başlık/stamp gizleniyor,
  hash'ler kısaltılıyor (media query'ler 900/560px).

## 17b. Order sayfası: ödeme kartı + kırmızı badge + ticket 500 kök nedeni

1. **Order detayında "Pay now" kartı:** `awaiting_payment` siparişlerde sayfa
   yenilense bile ödeme kartı GÖRÜNÜR — saklanan Lightning faturasından
   kurulan "Pay with Nimiq Pay Lightning" linki + "Copy Lightning invoice"
   + "⏳ Time left to pay: 30m 00s" geri sayımı. Süre bitince link pasifleşir.
   (Eskiden reload sonrası ÖDEME YOLU tamamen kayboluyordu.)
2. **Kırmızı awaiting-payment badge:** Orders sayfası başlığında
   "🔴 N awaiting payment" hapı + topbar/tabbar Orders linkinde kırmızı
   "N" badge (sessionStorage + event ile her sayfadan görünür; sepete
   ürün eklenince de artar).
3. **Ticket 500 kök nedeni (GERÇEK BUG):** `POST /api/support/tickets`
   quote-only alıcılar için `GetUser` kaydı bulamayınca 500 "could not
   verify user" ile ÇÖKÜYORDU — yani o alıcılar destek ticket'ı HİÇ
   açamıyordu. GetUser artık best-effort (adres sadece gösterim); ticket
   201 oluşturuluyor. Ek olarak order/quote detayı artık her render'da
   GERÇEK ticket durumunu çekiyor (eskiden quote detayı hep null geçiyordu
   → reload'da ticket "kayboluyor", tekrar açmaya zorluyordu).
   Kanıt: create 201 → GET ticket'ı döndürüyor → order sayfası reload'da
   ticket'ı gösteriyor, "Open a support ticket" dup butonu YOK.

## 18. Yeni sekme, yumuşak animasyon, güçlü contact

1. **Ana sayfa kartları GERÇEK link:** `<div role=button>` yerine `<a href>` —
   **sağ tık → "Yeni sekmede aç"**, orta tık ve link kopyalama native çalışır;
   sol tık hâlâ anında SPA geçişi, hover-prefetch aynen duruyor. Stokta
   olmayanlar tıklamayı blokluyor.
2. **Yumuşak giriş animasyonu (site geneli):** sert geçiş yerine içerik
   artık kayarak-aydınlanarak geliyor (`rise-in`: opacity + 10px yükselme,
   ~0.25s ease-out) — her sayfa mount'unda, SPA geçişlerinde ve tüm
   render'larda (`prefers-reduced-motion` destekli).
3. **Güçlü contact/ticket:** destek sayfasına hero chips ("Fast, human
   replies" / "Every ticket is tied to your order" / "Private"), yeni ticket
   formuna **Quick topic** çipleri (Code not working / Payment issue /
   Delivery delay / Refund question — tek tıkla konu doldurur). Chat
   avatarları + gönderen adları önceki sürümden aynen.
   jsdom kanıtı: hero 3 chip ✅, 4 topic chip ✅, chip tıklayınca subject
   "Code not working" doldu ✅.

## 19. "Listede var ama product not found" — kendini iyileştiren liste

Kök neden: /v2/brands (dizin) ile /v5/products (ürün listesi) FARKLI supplier
uç noktaları — bir marka dizinde dururken ürün listesi boş olabilir (ürün
kaldırılmış, bölge kilidi, supplier veri tutarsızlığı). Ana sayfa dizinden
beslendiği için kart görünüyordu, ürün sayfası ise 404 veriyordu — sonsuz
döngü.

**Çözüm — tombstone (kendini iyileştiren liste):**
- Bir family "boş" kanıtlanınca (sağlam ürün verisi hiç yoksa) 45 dakikalık
  tombstone kaydedilir.
- PUBLIC marka listeleri tombstone'lu kartları DROP eder — kimse artık o
  ölü uca tıklamaz. (Admin listesi ham veriyi görür.)
- Ürün geri geldiği an tombstone silinir, kart listeye DÖNER.
- Frontend'de de session-level hafıza: ürün sayfası "not available" görünce
  o kart bu oturumda listeden filtrelenir.

**Kanıt (canlı):** listede 7 marka + ghost ✅ → ghost ürün sayfası 404 ✅ →
 listede ghost DÜŞTÜ (6 marka) ✅ → sağlıklı ürün 200 yerinde ✅

## 20. Deploy turu düzeltmeleri (Windows + tunnel)

1. **home.js çökmesi (KRİTİK):** `isMissingProduct is not defined` — kendini
   iyileştiren liste filtresi renderGrid'de kullanılmış ama yardımcı
   fonksiyonlar dosyaya eklenmemişti. Eklendi; ana sayfa 7 kartla hatasız
   açılıyor (jsdom doğrulandı).
2. **`/api/market/nim-rate` 503 (cold boot):** oran snapshot'ı artık
   Badger'a kaydediliyor ve RESTART sonrası ilk istek bile sıcak dönüyor
   (ölçüm: restart → ilk istek 200, age_seconds 0). 503 yalnızca İLK KEZ
   hiç çalışan bir deployment'ta birkaç saniye görünebilir.
3. **Windows başlatıcı:** backend çıktısı artık `backend/backend.log`
   dosyasına yazılıyor + pencerede kalıyor; başlatıcı backend sağlık
   kontrolünü 30 sn bekler, çökerse logun SON 25 SATIRINI ekrana basıp
   durur — hata artık kaybolmuyor.

## 21. Nimiq Pay Lightning entegrasyonu (resmi URL davranışı)

Nimiq'in resmi dokümanındaki mekanizmalarla (nimiq.dev) üç katmanlı ödeme
entegrasyonu — `lightningPayBlock()` (ui.js) ile ÜÇ ödeme ekranında
(ürün sayfası, ana sayfa, sepet, sipariş detayı):

1. **Telefonda:** altın link `lightning:` URI'si açar — Nimiq Pay bu
   standardı (BOLT11 URI scheme) kayıtlı tutar, fatura UYGULAMADA açılır.
   Mobilde buton etiketi "Pay with Nimiq Pay — opens app".
2. **Masaüstünde:** `lightning:` handler'ı yoktur (beklenen davranış) —
   **"Show QR"** butonu faturanın QR'ını açar, telefondaki Nimiq Pay ile
   taranır. + "Copy Lightning invoice".
3. **Nimiq Pay mini-app handoff:** `nimiqpay://miniapp?url=` (resmi scheme)
   mevcut "Open in Nimiq Pay" butonlarında aynen durur — site Nimiq Pay
   içinde de tam çalışır.

Kanıt: order detay sayfası jsdom — lightning: link ✅ · QR butonu ✅ ·
QR açılıyor ✅ · copy ✅ · geri sayım ✅

## 22. Windows launcher (start.bat) son hali

backend.log dogrulandi: backend TAMAMEN SAGLIKLI (listening + 4 ulke
cachewarm basarili, hicbir hata yok).

- Backend penceresi artik bos gorunmez: ayri bir **"nim.shop backend -
  LIVE LOG"** penceresi acilir ve log CANLI akar (backend.log'a da yazilir).
- Backend cokerse kendi penceresinde pause ile hata gorunur kalir.
- Health check: PowerShell yoksa varligi varsaymak yerine "skipping health
  check" ile normal akisa devam eder; hata varsa logun son 25 satiri + dur.
- Tum baslatici mesajlari Ingilizce/ASCII.

## 23. Ilk acilista sayfanin ortasinda/basinda uyaniyor

Tarayici reload'da onceki scroll konumunu geri yukluyordu — sayfa iskeleti
kisa iken konum clamp'laniyor, icerik gelince de ziyaretci sayfanin rastgele
bir yerinde uyaniyordu. `history.scrollRestoration = 'manual'` + acilista
scrollTo(0,0) (bootShell'de de) ile ilk acilis HEP EN USTTEN baslar.

## 24. home.js syntax hatasi (URGENT fix)

`drawQuoteStep` icinde `const pay = lightningPayBlock(...)` yanlislikla
`body.append(...)` cagrisinin ORTASINA enjekte edilmisti → "Unexpected
token 'const'" → ana sayfa tamamen bos kaliyordu (sadece footer).
Fonksiyon temiz yeniden yazildi. Ayarla birlikte syntax kontrolu artik
GERCEK ESM import ile yapiyor (node --check bu Node surumunde modul
syntax hatalarini PASS ediyordu — yanilticiydi). Tum js dosyalari
gercek import taramasindan geci: HEPSI PARSE TEMIZ.
jsdom kaniti: ana sayfa 7 kart, kartlar <A> linki (right-click new-tab
calisir), href dogru.

## 25. Urun sayfasinda supplier'in GERCEK icerigi (How to redeem / T&C / bolge)

Gercek CryptoRefills API'sinden ornek cekildi (Hepsiburada TR) — urun
nesnesinde `rich_description` nesnesi var:
  - description (HTML)        → urun aciklamasi
  - how_to_redeem (HTML)      → supplier'in KENDI "How to redeem" adimlari
  - term_and_conditions (HTML)→ supplier'in KENDI "Terms and conditions"
  - redeem_geo                → "May only be redeemable in TR" bolge siniri
  - note / brand_url          → ek notlar

Entegrasyon:
- Backend: `RichDescription` struct'i Family'ye eklendi, aynen passthrough.
- Frontend: `safeRichHTML()` sanitizer (script/iframe/on*/javascript:
  temizler, linkleri target=_blank + noopener yapar) ile urun sayfasinda:
    * how-to-redeem VARSA supplier'in kendi adimlari gosterilir (yoksa
      genel adimlar)
    * "Terms and conditions (from CryptoRefills)" karti (linkler
      tiklanabilir, yeni sekmede)
    * "🌍 May only be redeemable in TR + Not in TR? Find your country"
      banner'i bayrakla
Kanim: canli mock'ta supplier how-to HTML sayfada gorunur, geo banner VAR,
linkler target=_blank, script/javascript: temiz.

## 26. "Not available" karti ortalandi

Arama ikonu sola dusuyordu (.center yalniz text-align veriyordu, svg
display:block oldugu icin ortalanmiyordu). Kart artik sitenin standart
.empty bicimini kullaniyor: yuvarlak damgali ikon ORTALI, baslik/metin/
butonlar hizali — ayni gorunum sitenin her bos-durum ekraninda.

## 27. QR kart tasarimi

QR kod artik cirak bir div'de degil — sitenin kraft tasarimina uyan
cerceveli kartta: dashed border, beyaz zemin (tarayici icin kontrast),
golge, ortalanmis 216x216 boyut, "Scan with Nimiq Pay" aciklamasi altinda.
Butonlarla arasinda nefes payi (mt-3) var.
