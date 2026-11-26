# Kod imzalama politikası / Code signing policy

ZETX BATTERY'nin resmi Windows dosyaları (`ZETX_BATTERY_Kurulum.exe`, `ZETX BATTERY.exe`) bu depodaki
kaynak koddan GitHub Actions ile (`.github/workflows/build.yml`) derlenir.

Official Windows binaries are built from this public repository by GitHub Actions.
Signing is planned through the SignPath Foundation open-source program:
"Free code signing provided by SignPath.io, certificate by SignPath Foundation" (once approved).

- Proje sahibi / Project owner and approver: Volkan Sezer (@wolkansezer)
- Yalnızca bu depodan derlenen dosyalar imzalanır. / Only artifacts built from this repository are signed.
- Gizlilik: Program internete veri göndermez; yalnızca bilgisayardaki cihazlardan ve (açıksa) yerel
  G HUB servisinden (localhost) pil bilgisini okur. / Privacy: the program sends no data over the
  network; it only reads battery levels from local devices and, if running, the local G HUB service.
