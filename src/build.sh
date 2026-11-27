#!/usr/bin/env bash
# Cross-compile ZETX BATTERY and its setup for Windows x64.
# Optional: go install github.com/tc-hib/go-winres@latest  -> embeds icon, version info and manifest.
set -euo pipefail
cd "$(dirname "$0")"
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
VERSION=1.2.1
go mod tidy
mkdir -p dist
if command -v go-winres >/dev/null; then
  go-winres simply --icon installer/payload/app.ico --manifest gui --product-name "ZETX BATTERY" \
    --file-description "ZETX BATTERY - kablosuz cihaz pil gostergesi" --product-version $VERSION --file-version $VERSION \
    --company-name "Volkan Sezer (ZETX)" --copyright "(c) 2026 Volkan Sezer - MIT" --original-filename "ZETX BATTERY.exe"
  (cd installer && go-winres simply --icon payload/app.ico --manifest gui --product-name "ZETX BATTERY Kurulum" \
    --file-description "ZETX BATTERY kurulum programi" --product-version $VERSION --file-version $VERSION \
    --company-name "Volkan Sezer (ZETX)" --copyright "(c) 2026 Volkan Sezer - MIT" --original-filename "ZETX_BATTERY_Kurulum.exe")
fi
go build -trimpath -ldflags "-H windowsgui" -o installer/payload/app.exe .
cp installer/payload/app.exe "dist/ZETX BATTERY.exe"
go build -trimpath -ldflags "-H windowsgui" -o dist/ZETX_BATTERY_Kurulum.exe ./installer
echo "Hazır: dist/ZETX_BATTERY_Kurulum.exe ve dist/ZETX BATTERY.exe"
