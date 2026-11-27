# Build ZETX BATTERY, its setup and the portable zip on Windows.
# Needs Go 1.24+ (https://go.dev/dl/). Optional:
#   go install github.com/tc-hib/go-winres@latest   -> icon, version info and manifest in the exe
#   Inno Setup 6 (https://jrsoftware.org/isdl.php)  -> dist\ZETX_BATTERY_Kurulum.exe
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
$version = "1.4.4"
go mod tidy
New-Item -ItemType Directory -Force dist | Out-Null
if (Get-Command go-winres -ErrorAction SilentlyContinue) {
  go-winres simply --icon app.ico --manifest gui --product-name "ZETX BATTERY" `
    --file-description "ZETX BATTERY - kablosuz cihaz pil gostergesi" --product-version $version --file-version $version `
    --company-name "Volkan Sezer (ZETX)" --copyright "(c) 2026 Volkan Sezer - MIT" --original-filename "ZETX BATTERY.exe"
}
go build -trimpath -ldflags "-H windowsgui" -o "dist/ZETX BATTERY.exe" .
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

# Portable version: the exe alone in a zip
Compress-Archive -Path "dist/ZETX BATTERY.exe" -DestinationPath "dist/ZETX_BATTERY_Tasinabilir.zip" -Force

# Setup: built with Inno Setup, the installer framework antivirus engines know
$iscc = (Get-Command iscc -ErrorAction SilentlyContinue).Source
if (-not $iscc) {
  $iscc = Get-ChildItem "${env:ProgramFiles(x86)}\Inno Setup*\ISCC.exe", "$env:ProgramFiles\Inno Setup*\ISCC.exe" -ErrorAction SilentlyContinue |
    Select-Object -First 1 -ExpandProperty FullName
}
if ($iscc) {
  & $iscc "/DAppVersion=$version" /Qp setup.iss
  if ($LASTEXITCODE -ne 0) { throw "Inno Setup failed" }
  Write-Host "Hazır: dist\ZETX_BATTERY_Kurulum.exe, dist\ZETX_BATTERY_Tasinabilir.zip"
} else {
  Write-Warning "Inno Setup bulunamadı: kurulum dosyası üretilmedi (yalnızca dist\ZETX BATTERY.exe ve taşınabilir zip)."
}
