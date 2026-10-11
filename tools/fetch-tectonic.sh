#!/usr/bin/env bash
# Lädt Tectonic (LaTeX → PDF) für das Plugin template herunter und legt es
# gzip-komprimiert zum Einbetten ab: cmd/plugins/template/internal/tectonic/bin/<os>-<arch>/tectonic[.exe].gz
# (nicht im Git). Fehlt die Datei, baut das Plugin trotzdem – dann gilt tectonic_path
# bzw. tectonic im Suchpfad.
#   tools/fetch-tectonic.sh              für die eigene Plattform (go env GOOS/GOARCH)
#   tools/fetch-tectonic.sh linux amd64  für eine bestimmte
#   tools/fetch-tectonic.sh all          für alle unterstützten
# Umgebung: TECTONIC_VERSION (Standard 0.17.0), FETCH_RETRIES (Standard 5), FETCH_WAIT (Sekunden, Standard 5).
set -euo pipefail
cd "$(dirname "$0")/.."

version="${TECTONIC_VERSION:-0.17.0}"
retries="${FETCH_RETRIES:-5}"
wait="${FETCH_WAIT:-5}"
dest=cmd/plugins/template/internal/tectonic/bin

asset() {
    case "$1-$2" in
        linux-amd64) echo "x86_64-unknown-linux-musl.tar.gz" ;;
        linux-arm64) echo "aarch64-unknown-linux-musl.tar.gz" ;;
        darwin-amd64) echo "x86_64-apple-darwin.tar.gz" ;;
        darwin-arm64) echo "aarch64-apple-darwin.tar.gz" ;;
        windows-amd64) echo "x86_64-pc-windows-msvc.zip" ;;
        *) return 1 ;;
    esac
}

fetch() {
    local os=$1 arch=$2 a exe="" out tmp
    a=$(asset "$os" "$arch") || { echo "Tectonic: keine Ausgabe für $os-$arch" >&2; return 0; }
    [ "$os" = windows ] && exe=".exe"
    out="$dest/$os-$arch/tectonic$exe.gz"
    if [ -s "$out" ] && [ -s "$out.version" ] && [ "$(cat "$out.version")" = "$version" ]; then
        echo "Tectonic $version für $os-$arch vorhanden"
        return 0
    fi
    url="https://github.com/tectonic-typesetting/tectonic/releases/download/tectonic%40$version/tectonic-$version-$a"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' RETURN
    local n=1
    until curl -fsSL --connect-timeout 20 --max-time 600 -o "$tmp/pkg" "$url"; do
        if [ "$n" -ge "$retries" ]; then
            echo "Tectonic: Download fehlgeschlagen nach $n Versuchen: $url" >&2
            return 1
        fi
        echo "Tectonic: Versuch $n fehlgeschlagen, neuer Versuch in ${wait}s …" >&2
        sleep "$wait"
        n=$((n + 1))
    done
    case "$a" in
        *.zip) (cd "$tmp" && unzip -q pkg) ;;
        *) tar -xzf "$tmp/pkg" -C "$tmp" ;;
    esac
    mkdir -p "$dest/$os-$arch"
    gzip -9 -c "$tmp/tectonic$exe" > "$out"
    echo "$version" > "$out.version"
    echo "Tectonic $version für $os-$arch: $out"
}

case "${1:-}" in
    all) for p in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64; do fetch "${p%-*}" "${p#*-}"; done ;;
    "") fetch "$(go env GOOS)" "$(go env GOARCH)" ;;
    *) fetch "$1" "${2:?Architektur fehlt}" ;;
esac
