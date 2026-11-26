#!/usr/bin/env bash
# Cross-compile ZETX BATTERY and its setup for Windows x64 (works on Linux, macOS or Windows/Git Bash).
set -euo pipefail
cd "$(dirname "$0")"
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
go mod tidy
mkdir -p dist
go build -trimpath -ldflags "-H windowsgui -s -w" -o installer/payload/app.exe .
cp installer/payload/app.exe "dist/ZETX BATTERY.exe"
go build -trimpath -ldflags "-H windowsgui -s -w" -o dist/ZETX_BATTERY_Kurulum.exe ./installer
echo "Hazır: dist/ZETX_BATTERY_Kurulum.exe ve dist/ZETX BATTERY.exe"
