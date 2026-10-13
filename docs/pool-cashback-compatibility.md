# Havuz ve cashback uyumluluk doğrulaması

İncelenen havuz: `emrealt34/NimiqBase`, commit `894fe31b541c13045dbc0993b060745242942053`.

## Geçerli kural

%1 temel cashback mağaza bütçesinden devam eder. **Ek cashback**, ilgili kullanıcının havuzda ürettiği gerçekleşmiş komisyonun kredilendirilen kısmını aşamaz. Stake miktarı, APY tahmini veya gelecekteki epoch ödülü tek başına kredi oluşturmaz.

Havuzun `/api/cashback/profit` yanıtındaki `pool_fee_luna`, checkpoint ödülünden ayrılan ve kullanıcıya atfedilmiş tam Luna tutarıdır. Shop bu tutarın sadece daha önce tüketilmemiş artışını, programın kazanç payı ve havuzun sadakat çarpanıyla kredilendirir. Aynı kazancı tekrar okumak yeni hak oluşturmaz. Temel oran bu ek-kazanç sınırından bağımsızdır.

## Düzeltilen hatalar

- Aylık toplamın düşüp eski değerine dönmesi aynı kazancı yeniden kredilendirebiliyordu. Her adres/ay için kalıcı, yalnızca yükselen bir kayıt eklendi. Eski ayın tekrar gelmesi veya sunucu yeniden başlatılması kredi üretmez.
- Havuz yanıtının adresi, dönemi, UTC aralığı ve zorunlu sayısal alanları doğrulanmıyordu. Eksik, yanlış kullanıcıya ait veya geçersiz yanıt artık tüketilmez. Ay kimliği yanıttaki gerçek aralıktan alınır.
- Gerçek testnet RPC'si henüz yayınlanmamış hash için `-32603 Internal error` döndüğünde worker ilk ödemeyi hiç göndermiyordu. Sorgu hatasında sadece önceden saklanmış **aynı imzalı işlem** yeniden gönderilebilir. Sorgu hatası ödeme onayı veya yeni imza üretme izni sayılmaz. Süresi dolan işlemin değiştirilmesi için mevcut kesinleşmiş ispat şartı korunur.

- Defter taraması, JSON kayıt değerini indeks kimliği sanıyordu; bu nedenle admin listesi ve toplu defter bakımı kayıtları göremiyordu. Gerçek defter kayıtlarını okuyan tarama ve liste regresyon testi eklendi.

- Havuzun eksik epoch snapshot’u yanlışlıkla kullanıcının çıkışı sayılabiliyordu. NimiqBase artık bu aşamada `503` döndürür; shop mevcut krediyi silmez. Tamamlanmış snapshot’ta gerçekten bulunmayan staker için sıfır sonucu korunur.

## Yapılan doğrulamalar

- NimiqBase Go testleri `-race` ile çalıştırıldı.
- Aynı ayın tekrar okunması, gerileyen toplam, eski ayın yeniden gelmesi, geç gelen gerçek artış, eşzamanlı istekler, yeniden başlatma ve sıfır kazanç sınandı.
- NimiqBase'in gerçek API kodu ve gerçek veritabanı yerelde çalıştırıldı; shop HTTP istemcisi ve kredi defteri bu sunucuyla birlikte test edildi. Geçerli feed anahtarı, yanlış anahtar, kullanıcı adresi, dönem, Luna dönüşümü ve aynı kazancın ikinci kez tüketilmemesi doğrulandı.
- Gerçek Nimiq testnetinde geçici test cüzdanları ve faucet fonları kullanıldı. İki ayrı 1 NIM ödeme yapıldı. İlk yayın cevabı bilerek kaybettirildi; kuyruk aynı işlem kimliğiyle toparlandı. Worker tekrar çalıştırıldığında alıcı bakiyesi **tam 200000 Luna (2 NIM)** kaldı.

Kesinleşen testnet işlem hash'leri:

```text
16b829152d25c4817f4206dac5ce6ae0aedd742212e13bc550bc134105db3065
f6c855e69d2ce6a9c6bd74a59b7f5c35b5f3e9238ea8682354ca2fc773ca5dd1
```

İlk gerçek ağ denemesi gönderim kilitlenmesini ortaya çıkardı; yukarıdaki sonuçlar düzeltmeden sonraki başarılı denemeye aittir. Testnet ödeme testi bir validator kurup yeni epoch ödülü üretmez; havuzun kazanç API'siyle sözleşme testi ayrı bir yerel entegrasyon testidir. Bu sonuçlar bütün olası ağ koşullarında sıfır hata garantisi değildir.

## Tekrar çalıştırma

```sh
python scripts/integration/test-pool-contract.py /path/to/NimiqBase
cd backend
go test -race ./...
NIMSHOP_RUN_TESTNET=1 go test ./internal/cashback/worker -run '^TestLiveNimiqTestnetPayoutRecovery$' -v -count=1 -timeout=8m
```

İlk komut yalnızca bellek içindeki fixture veritabanını kullanır. Son komut gerçek testnet işlemleri yapar; üretim anahtarlarını okumaz. Go farklı konumdaysa yerel entegrasyon komutunda `NIMSHOP_GO_BIN` belirtilebilir.
