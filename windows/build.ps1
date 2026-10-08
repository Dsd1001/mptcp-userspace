param(
  [string]$Version = "",
  [switch]$SkipInstaller
)
$ErrorActionPreference = "Stop"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($Version)) {
  $Version = (Get-Content (Join-Path $Root "macos\VERSION") -Raw).Trim()
}
$InfoPath = Join-Path $PSScriptRoot "build\windows\info.json"
$Info = Get-Content $InfoPath -Raw | ConvertFrom-Json
if ($Info.productVersion -ne $Version) {
  throw "Windows info.json productVersion $($Info.productVersion) differs from suite version $Version"
}
$WailsConfig = Get-Content (Join-Path $PSScriptRoot "wails.json") -Raw | ConvertFrom-Json
if ($WailsConfig.info.productVersion -ne $Version) {
  throw "wails.json productVersion $($WailsConfig.info.productVersion) differs from suite version $Version"
}
$Bin = Join-Path $PSScriptRoot "build\bin"
New-Item -ItemType Directory -Force -Path $Bin | Out-Null

$SourceID = (& python (Join-Path $Root "scripts\source-manifest.py") --id).Trim()
if ([string]::IsNullOrWhiteSpace($SourceID)) { throw "Unable to compute Source-ID" }

# Wails -clean owns build/bin. Build the engine outside that directory first,
# compile its exact SHA256 into the GUI, then copy the same engine bytes back.
$EngineStage = Join-Path $env:TEMP ("mptcp-engine-" + [Guid]::NewGuid().ToString("N") + ".exe")
try {
  Push-Location (Join-Path $Root "macos\engine")
  try {
    $env:CGO_ENABLED = "0"
    go build -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X mptcp-desktop/engine/multipath.SourceID=$SourceID" -o $EngineStage .
    if ($LASTEXITCODE -ne 0) { throw "engine build failed" }
  } finally { Pop-Location }
  $EngineHash = (Get-FileHash $EngineStage -Algorithm SHA256).Hash.ToLowerInvariant()

  Push-Location $PSScriptRoot
  try {
    go mod download
    $Wails = Join-Path (go env GOPATH) "bin\wails.exe"
    if (-not (Test-Path $Wails)) {
      go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0
      if ($LASTEXITCODE -ne 0) { throw "wails install failed" }
    }
    & $Wails build -clean -platform windows/amd64 -webview2 embed -ldflags "-X main.appVersion=$Version -X main.expectedEngineSHA256=$EngineHash"
    if ($LASTEXITCODE -ne 0) { throw "wails build failed" }
  } finally { Pop-Location }

  New-Item -ItemType Directory -Force -Path $Bin | Out-Null
  Copy-Item $EngineStage (Join-Path $Bin "mptcp-engine.exe") -Force
} finally {
  Remove-Item $EngineStage -Force -ErrorAction SilentlyContinue
}

$Gui = Join-Path $Bin "MPTCP-Desk-Windows.exe"
$Engine = Join-Path $Bin "mptcp-engine.exe"
if (-not (Test-Path $Gui)) { throw "Wails output missing: $Gui" }
if (-not (Test-Path $Engine)) { throw "Engine output missing: $Engine" }

$GuiHash = (Get-FileHash $Gui -Algorithm SHA256).Hash.ToLowerInvariant()
$EngineHashAfterCopy = (Get-FileHash $Engine -Algorithm SHA256).Hash.ToLowerInvariant()
if ($EngineHashAfterCopy -ne $EngineHash) { throw "Engine bytes changed after Wails build" }

$BuildInfo = Join-Path $Bin "MPTCP-Desk-Windows.BUILDINFO"
@"
Component: MPTCP Desk Windows
Version: $Version
Source-ID: $SourceID
Protocol: MPX/4 Protocol Version 4 Stable
Platform: windows/amd64
Mode: userspace_multipath only
GUI-SHA256: $GuiHash
Engine-SHA256: $EngineHash
Updater: SHA256 + Ed25519 (same public key as macOS Sparkle channel)
"@ | Set-Content -Path $BuildInfo -Encoding UTF8

$Portable = Join-Path $Bin "MPTCP-Desk-$Version-Windows-Portable.zip"
if (Test-Path $Portable) { Remove-Item $Portable -Force }
Compress-Archive -Path $Gui,$Engine,$BuildInfo -DestinationPath $Portable

if (-not $SkipInstaller) {
  $MakeNSIS = (Get-Command makensis.exe -ErrorAction SilentlyContinue).Source
  if (-not $MakeNSIS) {
    $candidate = "C:\Program Files (x86)\NSIS\makensis.exe"
    if (Test-Path $candidate) { $MakeNSIS = $candidate }
  }
  if (-not $MakeNSIS) { throw "makensis.exe not found" }
  Push-Location $PSScriptRoot
  try {
    & $MakeNSIS "/DVERSION=$Version" "installer.nsi"
    if ($LASTEXITCODE -ne 0) { throw "NSIS build failed" }
  } finally { Pop-Location }
}

Get-FileHash $Gui -Algorithm SHA256 | Format-List
Get-FileHash $Engine -Algorithm SHA256 | Format-List
Get-FileHash $Portable -Algorithm SHA256 | Format-List
if (-not $SkipInstaller) {
  Get-FileHash (Join-Path $Bin "MPTCP-Desk-$Version-Windows-Setup.exe") -Algorithm SHA256 | Format-List
}
