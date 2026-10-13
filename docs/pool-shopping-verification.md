# Validator kazancı → alışveriş → cashback doğrulaması

13 Ekim 2026 tarihinde bağlantılı test geçti. Önceki ayrı havuz/API ve ödeme testlerinden farklı olarak bu test, aynı gerçek yerel validator kazancını mağaza ekranına, alışverişe, cashback defterine ve kesinleşmiş NIM ödemesine kadar izler.

## Testin sınırı

İki resmi Nimiq 2.2.0 node'u **izole UnitAlbatross** ağında gerçek consensus, stake, epoch ödülü ve transfer üretir. Bu ağın NIM'i üretim fonu değildir; aşağıdaki hash'ler genel testnet veya mainnet explorer'ında bulunmaz. Bu sonuçlar üretim APY'si değildir.

Havuzun gerçekten ürettiği Badger veritabanının tutarlı yedeği, NimiqBase'in gerçek API koduyla açılır. Ödül, stake ve komisyon kayıtları uydurulmaz. Mağazanın gerçek router'ı, oturum doğrulaması, sipariş takibi, defteri, veritabanı ve cashback worker'ı çalışır. Chromium gerçek frontend'e bağlanır; mağaza API cevapları tarayıcıda taklit edilmez.

**Piyasa fiyatları, tedarikçinin Lightning tahsilat bildirimi ve hediye çeki teslimatı test doubles kullanır.** Cüzdanın harcama ayağı yerel basic NIM transferidir; gerçek Nimiq Pay atomic swap veya BTC Lightning tahsilatı sınanmamıştır. Gerçek ürün veya üretim cüzdanı kullanılmaz. Canlı VPS dağıtımı bu yerel testin parçası değildir.

## Bulunan ve düzeltilen hata

Normal alışverişin temel oranı `CashbackCodeBps` alanına yazılıyordu. Kod boş olmasına rağmen sonraki aşamalar bunu özel promosyon sayıyor ve kazanılmış havuz ek cashback'ini atlıyordu. Böylece kart %11 gösterebilirken sipariş %1 kazanabiliyordu.

- Tek ve toplu tekliflerde promosyon oranı yalnızca gerçek bir promosyon kodu için saklanır.
- Teklif yanıtı ve teslimat hesabı hem kodu hem oranı kontrol eder. Eski kayıtlarda kodsuz kalmış promosyon oranı da ek kazancı engellemez.
- Normal tekliflerin oranı kazanılmış ek krediyi içerir. Tahmini tutar, defterin bir kopyasında gerçek harcama sınırları ve kalan krediyle hesaplanır; teklif oluşturmak krediyi tüketmez.
- Ek kredi yalnızca teslimatın veritabanı işlemi içinde tüketilir. Tamamlanmış, önceden ödenmiş siparişlere bu değişiklik geriye dönük ödeme oluşturmaz.

## Gerçek bağlantılı sonuç

| Kontrol | Sonuç |
|---|---|
| Stake | 100 NIM, gerçek yerel validator'a delege edildi |
| Kazanılmış, kullanıcıya atfedilmiş havuz komisyonu | 28.396.579 Luna |
| Program sınırından sonra mağaza ek kredisi | 10 NIM |
| Ekrandaki oran | %11 → %5,98 → %1 |
| Alışveriş | Tarayıcıdan üç checkout; miktarlar 1, 2, 1 |
| Birinci cashback | 552.145 Luna |
| İkinci cashback | 598.440 Luna |
| Üçüncü cashback | 50.195 Luna |
| Toplam ödeme | **1.200.780 Luna = 12,0078 NIM** |
| Bunun kazanılmış ek kredi kısmı | **1.000.000 Luna = 10 NIM** |
| Alıcının gerçek bakiye artışı | **1.200.780 Luna**, ödeme toplamıyla eşit |
| Son ek kredi | 0 NIM |
| Worker yeniden başlatmaları | 5; ek transfer oluşmadı |

Her alışverişte harcama tutarına %5 test cüzdan maliyeti eklenir. Cashback, bu daha yüksek cüzdan harcamasından değil mağazanın sakladığı ürün fiyatından hesaplanır. Harcama hash'i ve özel swap kimliği siparişte saklanır; hash bildirmek siparişi ödenmiş saymaz. Tedarikçi tahsilatı doğrulamadan cashback oluşmaz. Özel swap kimliği public takip yanıtına sızmaz. Tahsilat ve teslimat zamanları mevcut ve doğru sıradadır.

Ödeme cüzdanı başlangıçta sıfırdır: üç yükümlülük kuyrukta, telefonda ve masaüstünde bekleyen görünür. Cüzdan fonlandıktan sonra her ödeme gerçek macro finality ile kesinleşir. Her işlemde gönderici, alıcı, tam Luna tutarı ve memo kontrol edilir. Üç benzersiz hash ve alıcının bakiye farkı aynı sonucu verir. Tekrar fulfillment/refresh ve kazanç API'sini tekrar okumak yeni cashback veya kredi üretmez. Yanlış feed anahtarı mevcut krediyi silmez.

Kesinleşmiş yerel cashback hash'leri:

```text
05e108fad68b74cb8ca455ca58c7ceb1f15eca50d48b998731b91e323c1f0c09
780987fde6f81ac544aed1a2221463203dfbb2eb7289b1863f145f3d3e23755d
87538734fb836b9340f4ab436182fa25f9d30cfb3c4ee642ff15a4d10777fcc1
```

Masaüstü 1280×900 ve telefon 390×844 ekranlarında oran, pending/paid satır sayısı, JavaScript hatası ve yatay taşma kontrol edildi; ekran görüntüleri incelendi. Ham test çıktısı ve PNG'ler `test-results/pool-shopping/` altında oluşur; geçici cüzdan anahtarları bu çıktıya yazılmaz.

Ek regresyon testleri kodsuz eski/yeni siparişin ek kredi tüketmesini ve gerçek promosyonun ek krediyi tüketmeden özel oranıyla devam etmesini doğrular. Ayrı worker testleri kalıcı imza, kaybolan RPC cevabı, ani süreç kapanması, veritabanı yeniden açılması, eşzamanlı worker ve bakiye eksikliğini kapsar. Bunlar her olası koşul için sıfır hata garantisi değildir.

## Tekrar çalıştırma

Önce mağaza frontend'ini temiz çıktı klasörüne, aynı origin `/api` kullanacak şekilde derleyin:

```sh
cd /path/to/nimiqshop
API_URL= PUBLIC_BASE=/ npm run build
python /path/to/NimiqBase/pool/scripts/fetch-test-node.py /path/to/nimiq-client
cd /path/to/NimiqBase/pool
NIMIQBASE_NODE_BINARY=/path/to/nimiq-client \
NIMIQBASE_SHOP_FLOW_SCRIPT=/path/to/nimiqshop/scripts/integration/test-pool-shopping.py \
NIMIQBASE_CHECKOUT=/path/to/NimiqBase \
NIMSHOP_GO_BIN=/path/to/go \
NIMSHOP_BROWSER_EXECUTABLE=/path/to/chromium \
/path/to/go test -race ./internal/pool \
  -run '^TestLocalValidatorEpochRewardsAndStakerEarnings$' -v -count=1 -timeout=35m
```

Checkout'lar, API ve node'lar aynı test sürecinin alt süreçleridir; ayrı localhost ağları kurulmaz. Varsayılan olarak hata testin başarısız olmasını sağlar. Yalnız geliştirme sırasında `NIMSHOP_FLOW_DEBUG_RETRY=1` aynı kazanılmış fixture'ı tutar ve `test-results/pool-shopping/retry` dosyasıyla yeniden shop denemesi bekler. Normal CI bu pahalı gerçek-node testini açıkça etkinleştirmedikçe çalıştırmaz.
