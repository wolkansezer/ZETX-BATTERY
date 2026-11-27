# Build ZETX BATTERY and its setup on Windows (requires Go 1.24+: https://go.dev/dl/)
# Optional: go install github.com/tc-hib/go-winres@latest  -> embeds icon, version info and manifest.
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
$version = "1.4.1"
go mod tidy
New-Item -ItemType Directory -Force dist | Out-Null
if (Get-Command go-winres -ErrorAction SilentlyContinue) {
  go-winres simply --icon installer/payload/app.ico --manifest gui --product-name "ZETX BATTERY" `
    --file-description "ZETX BATTERY - kablosuz cihaz pil gostergesi" --product-version $version --file-version $version `
    --company-name "Volkan Sezer (ZETX)" --copyright "(c) 2026 Volkan Sezer - MIT" --original-filename "ZETX BATTERY.exe"
  Push-Location installer
  go-winres simply --icon payload/app.ico --manifest gui --product-name "ZETX BATTERY Kurulum" `
    --file-description "ZETX BATTERY kurulum programi" --product-version $version --file-version $version `
    --company-name "Volkan Sezer (ZETX)" --copyright "(c) 2026 Volkan Sezer - MIT" --original-filename "ZETX_BATTERY_Kurulum.exe"
  Pop-Location
}
go build -trimpath -ldflags "-H windowsgui" -o installer/payload/app.exe .
Copy-Item installer/payload/app.exe "dist/ZETX BATTERY.exe" -Force
go build -trimpath -ldflags "-H windowsgui" -o dist/ZETX_BATTERY_Kurulum.exe ./installer
Write-Host "Hazır: dist\ZETX_BATTERY_Kurulum.exe ve dist\ZETX BATTERY.exe"
