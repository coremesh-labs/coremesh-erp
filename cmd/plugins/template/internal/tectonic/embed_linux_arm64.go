package tectonic

import "embed"

//go:embed bin/linux-arm64
var files embed.FS

const platform = "linux-arm64"
