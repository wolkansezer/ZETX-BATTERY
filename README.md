<p align="center">
  <img src="images/banner.png" alt="ZETX BATTERY" width="100%">
</p>

<p align="center">
  <b>Kablosuz kulaklık, mouse ve klavyenin pil yüzdelerini Windows görev çubuğunda, saatin hemen yanında gösterir.</b><br>
  Pil azalınca bildirim gönderir. Ücretsizdir.
</p>

<p align="center">
  <a href="https://github.com/wolkansezer/zetx-battery/raw/main/indir/ZETX_BATTERY_v1.0.1.zip"><img src="https://img.shields.io/badge/%C4%B0ndir-ZETX%20BATTERY%20v1.0.1-ff7a00?style=for-the-badge&logo=windows" alt="İndir"></a>
  <img src="https://img.shields.io/badge/Windows-10%20%7C%2011-0078D6?style=for-the-badge" alt="Windows 10 | 11">
  <a href="https://discord.gg/nvqc84AHZf"><img src="https://img.shields.io/badge/Discord-Destek-5865F2?style=for-the-badge&logo=discord&logoColor=white" alt="Discord"></a>
</p>

---

## Görev çubuğunda

<p align="center"><img src="images/taskbar.png" alt="Görev çubuğu görünümü" width="100%"></p>

Kulaklık, mouse ve klavye simgeleri ile pil yüzdeleri, Windows saatinin solunda görev çubuğunun bir parçası gibi durur. Aynı türden birden fazla cihaz varsa (örneğin iki klavye) simgenin yanında küçük **1**, **2** numaraları görünür. Oyun veya video tam ekrandayken gösterge kendiliğinden gizlenir.

## Durumlar tek bakışta

<p align="center"><img src="images/durumlar.png" alt="Gösterge durumları" width="100%"></p>

| Görünüm | Anlamı |
|---|---|
| Beyaz yüzde | Normal |
| Kırmızı yüzde | Pil, belirlediğiniz eşiğin altında |
| Yeşil şimşek | Şarj oluyor |
| Soluk | Cihaz uykuda veya kapalı (son bilinen değer) |
| `--` | Cihaz bağlı ama pil bilgisi göndermiyor |

## Bildirimler

<p align="center"><img src="images/bildirim.png" alt="Düşük pil bildirimi" width="70%"></p>

- Pil seçtiğiniz eşiğe (%10, %15, %20 veya %30) düşünce Windows bildirimi gelir.
- %5'te ikinci bir **kritik pil** uyarısı gelir.
- İsterseniz şarj tamamlanınca da bildirim alırsınız.

## Ayarlar

<p align="center"><img src="images/menu.png" alt="Ayarlar menüsü" width="60%"></p>

Saatin yanındaki pil simgesine **sağ tıklayın**:

- **Cihazlar:** Göstergede görmek istemediğiniz cihazın işaretini kaldırın (örneğin Bluetooth'lu telefon).
- **Görev çubuğunda / Masaüstünde serbest:** Göstergenin yerini seçin.
- **Saat ve tarihi göster:** Saati göstergeye ekler.
- **Düşük pil bildirimi**, **Şarj dolunca bildir**, **Bildirimi test et**
- **Windows ile başlat**, **Şimdi yenile**, **Tanılama raporunu aç**

## Masaüstü modu

<p align="center"><img src="images/masaustu.png" alt="Masaüstü modu" width="100%"></p>

Göstergeyi görev çubuğundan çıkarıp sürükleyerek ekranda istediğiniz yere bırakabilirsiniz. Koyu arka planı açıp kapatabilirsiniz.

## Desteklenen cihazlar

<p align="center"><img src="images/destek.png" alt="Desteklenen cihazlar" width="100%"></p>

- **Logitech (HID++):** Lightspeed, Unifying ve Bolt alıcıları, G serisi kablosuz kulaklıklar, USB veya Bluetooth ile bağlı Logitech cihazlar. G HUB açıkken de çalışır.
- **Tüm markalar, Bluetooth / Bluetooth LE:** Windows'un bildirdiği pil seviyesi ve BLE pil servisi okunur.
- **Standart HID:** Pil bilgisini standart HID raporuyla gönderen USB kablosuz alıcılar.

> Not: Razer, Corsair ve SteelSeries gibi markaların kendi USB alıcıları pil bilgisini kendi özel yöntemleriyle gönderir. Bu alıcılara bağlı cihazların pil yüzdesi görünmeyebilir. Görünmeyen bir cihazınız varsa menüden **Tanılama raporunu aç** ile raporu alıp Discord'dan gönderin.

## Kurulum

1. **[ZETX_BATTERY_v1.0.1.zip dosyasını indirin](https://github.com/wolkansezer/zetx-battery/raw/main/indir/ZETX_BATTERY_v1.0.1.zip)** (ya da depodaki `indir` klasöründen).
2. Zip'i bir klasöre çıkarın ve `ZETX BATTERY.exe` dosyasını çalıştırın.
3. Kurulum gerekmez. İlk açılışta program Windows ile birlikte başlayacak şekilde ayarlanır (menüden kapatılabilir).

Windows SmartScreen "Windows bilgisayarınızı korudu" uyarısı gösterirse **Ek bilgi → Yine de çalıştır** seçeneğini kullanın. Program imzasız olduğu için bu uyarı normaldir.

Ayarlar şu dosyada tutulur: `%APPDATA%\ZETX BATTERY\settings.json`

## Sürüm notları

**v1.0.1**
- Logitech alıcılarında mouse'un görünmemesi hatası düzeltildi.
- Uykudaki Logitech cihazlar da soluk olarak gösterilir.
- Pil bilgisi vermeyen bağlı Bluetooth klavye, mouse ve kulaklıklar `--` ile gösterilir.
- Menüye **Tanılama raporunu aç** eklendi.

**v1.0.0**
- İlk sürüm.

## İletişim

- **Sahibi:** Volkan Sezer
- **Discord:** [discord.gg/nvqc84AHZf](https://discord.gg/nvqc84AHZf)
- **Instagram:** [@zetxtv](https://instagram.com/zetxtv)
- **E-posta:** [volkan.sezer@outlook.com.tr](mailto:volkan.sezer@outlook.com.tr)
