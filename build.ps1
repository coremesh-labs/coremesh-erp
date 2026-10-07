# Windows ohne make: baut alle ERP-Plugins (wie "make build").
#   .\build.ps1          bauen
#   .\build.ps1 -Test    vet + Tests
#   .\build.ps1 -Run     bauen und den Host des Kerns mit beiden configs starten
param([switch]$Test, [switch]$Run)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$plugins = @(
    @{ Name = 'ledger'; Version = '0.3.0'; Path = './cmd/plugins/ledger' }
)

if ($Test) {
    go vet ./...; if ($LASTEXITCODE) { exit $LASTEXITCODE }
    go test ./...; exit $LASTEXITCODE
}
$os = go env GOOS; $arch = go env GOARCH
$ext = if ($os -eq 'windows') { '.exe' } else { '' }
foreach ($p in $plugins) {
    $out = "bin/plugins/$($p.Name.Substring(0,2))/$($p.Name)-$($p.Version)-$os-$arch$ext"
    go build -o $out $p.Path; if ($LASTEXITCODE) { exit $LASTEXITCODE }
    Write-Host "gebaut: $out"
}
if ($Run) {
    Set-Location ../coremesh
    & "./bin/host$ext" -config 'configs,../coremesh-erp/configs'
}
