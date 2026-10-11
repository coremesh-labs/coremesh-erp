package tectonic

import "embed"

//go:embed bin/windows-amd64
var files embed.FS

const platform = "windows-amd64"
