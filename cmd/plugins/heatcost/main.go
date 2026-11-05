// Command heatcost ist das Plugin der Heizkostenabrechnung (Modul heatcost,
// internal/heatcost): Heiz- und Warmwasserkosten einer Liegenschaft nach
// HeizkostenV verteilen – Kostenaufstellung, Warmwasseranteil, Grund- und
// Verbrauchskosten, Nutzergruppen, Hausnebenkosten nach eigenem Schlüssel, Werte
// aus Zählern (techobj) und Flächen (realestate), Ergebnis je Nutzer, Korrektur
// als neue Abrechnung.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/heatcost/internal/heatcost"
)

const version = "0.1.9"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "heatcost", Version: version, Description: "Heizkostenabrechnung: Heiz- und Warmwasserkosten nach HeizkostenV verteilen"},
		heatcost.New(),
	))
}
