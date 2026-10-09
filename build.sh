#!/usr/bin/env bash
# Linux/macOS ohne make: baut alle ERP-Plugins (wie "make build", Gegenstück zu build.ps1).
#   ./build.sh          bauen
#   ./build.sh --test   vet + Tests
#   ./build.sh --run    bauen, den Host des Kerns bauen (falls nötig) und mit beiden configs starten
set -euo pipefail
cd "$(dirname "$0")"

plugins=(
    "ledger 0.13.4 ./cmd/plugins/ledger"
    "realestate 0.3.2 ./cmd/plugins/realestate"
    "contract 0.10.1 ./cmd/plugins/contract"
    "procurement 0.4.0 ./cmd/plugins/procurement"
    "opcost 0.3.0 ./cmd/plugins/opcost"
    "bank 0.1.0 ./cmd/plugins/bank"
)

case "${1:-}" in
    --test) go vet ./... && exec go test ./... ;;
    ""|--run) ;;
    *) echo "Aufruf: $0 [--test|--run]" >&2; exit 2 ;;
esac

os=$(go env GOOS); arch=$(go env GOARCH)
ext=""; [ "$os" = windows ] && ext=".exe"
for p in "${plugins[@]}"; do
    read -r name version path <<<"$p"
    out="bin/plugins/${name:0:2}/${name}-${version}-${os}-${arch}${ext}"
    go build -o "$out" "$path"
    echo "gebaut: $out"
done

if [ "${1:-}" = --run ]; then
    cd ../coremesh
    [ -x "./bin/host$ext" ] || make build
    exec "./bin/host$ext" -config configs,../coremesh-erp/configs
fi
