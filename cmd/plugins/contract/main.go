// Command contract ist das Plugin der Vertragsverwaltung (Modul contract,
// internal/contract): Verträge mit Partnern, Objekten, Konditionen und
// Kündigungsregeln – Mietverträge, Hausgeld, Dienstleistungs-, Versicherungs-
// und sonstige Verträge.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/contract/internal/contract"
)

const version = "0.10.3"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "contract", Version: version, Description: "Verträge: Partner, Objekte, Konditionen, Kündigung"},
		contract.New(),
	))
}
