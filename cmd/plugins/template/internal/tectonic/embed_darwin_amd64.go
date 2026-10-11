package tectonic

import "embed"

//go:embed bin/darwin-amd64
var files embed.FS

const platform = "darwin-amd64"
