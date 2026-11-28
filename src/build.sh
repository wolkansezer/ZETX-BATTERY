#!/usr/bin/env bash
# Cross-compile ZETX BATTERY for Windows x64 (the app exe and the portable zip).
# The setup program is built on Windows with Inno Setup: see build.ps1 / setup.iss.
# Optional: go install github.com/tc-hib/go-winres@latest  -> embeds icon, version info and manifest.
set -euo pipefail
cd "$(dirname "$0")"
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
VERSION=1.4.6
go mod tidy
mkdir -p dist
if command -v go-winres >/dev/null; then
  go-winres simply --icon app.ico --manifest gui --product-name "ZETX BATTERY" \
    --file-description "ZETX BATTERY - kablosuz cihaz pil gostergesi" --product-version $VERSION --file-version $VERSION \
    --company-name "Volkan Sezer (ZETX)" --copyright "(c) 2026 Volkan Sezer - MIT" --original-filename "ZETX BATTERY.exe"
fi
go build -trimpath -ldflags "-H windowsgui" -o "dist/ZETX BATTERY.exe" .
(cd dist && rm -f ZETX_BATTERY_Tasinabilir.zip && zip -q ZETX_BATTERY_Tasinabilir.zip "ZETX BATTERY.exe")
echo "Hazır: dist/ZETX BATTERY.exe ve dist/ZETX_BATTERY_Tasinabilir.zip"
