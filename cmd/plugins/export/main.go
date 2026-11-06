// Command export ist das Plugin „Sichten“ (Modul export, internal/export):
// Dokumente nach festgelegten Kriterien zusammenstellen (Sicht), zu einem
// Stichtag ausführen (Lauf mit Inhaltsverzeichnis und Manifest) und an ein
// Ziel ausgeben (Verzeichnis, zip).
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/export/internal/export"
)

const version = "0.3.1"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "export", Version: version, Description: "Sichten: Dokumente nach Kriterien zusammenstellen und exportieren"},
		export.New(),
	))
}
