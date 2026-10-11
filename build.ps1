# Windows ohne make: baut alle ERP-Plugins (wie "make build").
#   .\build.ps1          bauen
#   .\build.ps1 -Test    vet + Tests
#   .\build.ps1 -Run     bauen und den Host des Kerns mit beiden configs starten
param([switch]$Test, [switch]$Run)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$plugins = @(
    @{ Name = 'ledger'; Version = '0.24.0'; Path = './cmd/plugins/ledger' }
    @{ Name = 'realestate'; Version = '0.3.6'; Path = './cmd/plugins/realestate' }
    @{ Name = 'contract'; Version = '0.21.0'; Path = './cmd/plugins/contract' }
    @{ Name = 'procurement'; Version = '0.5.0'; Path = './cmd/plugins/procurement' }
    @{ Name = 'opcost'; Version = '0.3.4'; Path = './cmd/plugins/opcost' }
    @{ Name = 'bank'; Version = '0.1.5'; Path = './cmd/plugins/bank' }
    @{ Name = 'dms'; Version = '0.4.1'; Path = './cmd/plugins/dms' }
    @{ Name = 'template'; Version = '0.3.1'; Path = './cmd/plugins/template' }
)

# Tectonic (LaTeX → PDF) zum Einbetten in das Plugin template – wie tools/fetch-tectonic.sh,
# mit Wiederholungen ($env:FETCH_RETRIES, Standard 5).
function Get-Tectonic([string]$os, [string]$arch) {
    $version = if ($env:TECTONIC_VERSION) { $env:TECTONIC_VERSION } else { '0.17.0' }
    $assets = @{ 'windows-amd64' = 'x86_64-pc-windows-msvc.zip'; 'linux-amd64' = 'x86_64-unknown-linux-musl.tar.gz';
        'linux-arm64' = 'aarch64-unknown-linux-musl.tar.gz'; 'darwin-amd64' = 'x86_64-apple-darwin.tar.gz'; 'darwin-arm64' = 'aarch64-apple-darwin.tar.gz' }
    $asset = $assets["$os-$arch"]
    if (-not $asset) { Write-Host "Tectonic: keine Ausgabe für $os-$arch"; return }
    $exe = if ($os -eq 'windows') { 'tectonic.exe' } else { 'tectonic' }
    $dir = "cmd/plugins/template/internal/tectonic/bin/$os-$arch"
    $out = "$dir/$exe.gz"
    if ((Test-Path $out) -and (Test-Path "$out.version") -and ((Get-Content "$out.version") -eq $version)) { Write-Host "Tectonic $version vorhanden"; return }
    $url = "https://github.com/tectonic-typesetting/tectonic/releases/download/tectonic%40$version/tectonic-$version-$asset"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
    New-Item -ItemType Directory $tmp | Out-Null
    $retries = if ($env:FETCH_RETRIES) { [int]$env:FETCH_RETRIES } else { 5 }
    for ($n = 1; ; $n++) {
        try { Invoke-WebRequest -Uri $url -OutFile "$tmp/pkg" -UseBasicParsing; break }
        catch {
            if ($n -ge $retries) { throw "Tectonic: Download fehlgeschlagen nach $n Versuchen: $url" }
            Write-Host "Tectonic: Versuch $n fehlgeschlagen, neuer Versuch in 5 s …"; Start-Sleep -Seconds 5
        }
    }
    if ($asset.EndsWith('.zip')) { Expand-Archive "$tmp/pkg" -DestinationPath $tmp } else { tar -xzf "$tmp/pkg" -C $tmp }
    $in = [IO.File]::OpenRead("$tmp/$exe"); $fs = [IO.File]::Create($out)
    $gz = New-Object IO.Compression.GZipStream($fs, [IO.Compression.CompressionLevel]::Optimal)
    $in.CopyTo($gz); $gz.Close(); $in.Close()
    Set-Content "$out.version" $version
    Remove-Item -Recurse -Force $tmp
    Write-Host "Tectonic $version für $os-$arch`: $out"
}

if ($Test) {
    go vet ./...; if ($LASTEXITCODE) { exit $LASTEXITCODE }
    go test ./...; exit $LASTEXITCODE
}
$os = go env GOOS; $arch = go env GOARCH
$ext = if ($os -eq 'windows') { '.exe' } else { '' }
Get-Tectonic $os $arch
foreach ($p in $plugins) {
    $out = "bin/plugins/$($p.Name.Substring(0,2))/$($p.Name)-$($p.Version)-$os-$arch$ext"
    go build -o $out $p.Path; if ($LASTEXITCODE) { exit $LASTEXITCODE }
    Write-Host "gebaut: $out"
}
if ($Run) {
    Set-Location ../coremesh
    & "./bin/host$ext" -config 'configs,../coremesh-erp/configs'
}
