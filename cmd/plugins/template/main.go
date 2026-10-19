// Command template ist das Plugin der Vorlagen (Modul template, internal/templating):
// Ausgaben aus Go-Templates (HTML, XML, CSV, PDF über LaTeX/Tectonic) als Aufträge
// mit Nummer, Status-Event und Ergebnis zum Abholen.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/template/internal/templating"
)

const version = "0.7.0"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "template", Version: version, Description: "Vorlagen: Ausgaben (HTML, XML, CSV, PDF) aus Go-Templates als Aufträge"},
		templating.New(),
	))
}
