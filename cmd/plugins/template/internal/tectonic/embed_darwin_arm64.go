package tectonic

import "embed"

//go:embed bin/darwin-arm64
var files embed.FS

const platform = "darwin-arm64"
