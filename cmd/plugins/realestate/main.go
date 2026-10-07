// Command realestate ist das Plugin der Immobilienverwaltung (Modul realestate,
// internal/realestate): Wirtschaftseinheiten, Gebäude, Mietobjekte (Einheiten,
// Flächen, Pools, Vertragsobjekte), Bemessungen und Kataloge je Buchungskreis.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/realestate/internal/realestate"
)

const version = "0.2.0"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "realestate", Version: version, Description: "Immobilien: Wirtschaftseinheiten, Gebäude, Mietobjekte und Bemessungen"},
		realestate.New(),
	))
}
