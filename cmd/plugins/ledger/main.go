// Command ledger ist das Plugin der Finanzbuchhaltung (Modul accounting,
// internal/accounting): Kontenplan, Buchungsbelege nach dem Prinzip der
// doppelten Buchführung, Salden. Beschreibung: README.md.
package main

import (
	"github.com/camel/coremesh/pkg/sdk/module"
	"github.com/camel/coremesh/pkg/sdk/plugin"

	"github.com/camel/coremesh_erp/cmd/plugins/ledger/internal/accounting"
)

const version = "0.1.0"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "ledger", Version: version, Description: "Finanzbuchhaltung: Kontenplan, Buchungen, Salden"},
		accounting.New(),
	))
}
