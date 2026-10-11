package tectonic

import "embed"

//go:embed bin/linux-amd64
var files embed.FS

const platform = "linux-amd64"
