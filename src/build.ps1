# Build ZETX BATTERY and its setup on Windows (requires Go 1.24+: https://go.dev/dl/)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go mod tidy
New-Item -ItemType Directory -Force dist | Out-Null
go build -trimpath -ldflags "-H windowsgui -s -w" -o installer/payload/app.exe .
Copy-Item installer/payload/app.exe "dist/ZETX BATTERY.exe" -Force
go build -trimpath -ldflags "-H windowsgui -s -w" -o dist/ZETX_BATTERY_Kurulum.exe ./installer
Write-Host "Hazır: dist\ZETX_BATTERY_Kurulum.exe ve dist\ZETX BATTERY.exe"
