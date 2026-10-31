// Command asset ist das Plugin der Objekte (Modul asset, internal/asset):
// Wohnung, Auto, Reifensatz, Handy, Reise, Gerät … mit Bestandteilen,
// Personen in Rollen und Kataloge je Buchungskreis – für die Privat-Edition
// und überall, wo Verträge an etwas anderem als einem Mietobjekt hängen.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/asset/internal/asset"
)

const version = "0.3.0"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "asset", Version: version, Description: "Objekte: Wohnung, Auto, Handy, Reise … mit Bestandteilen und Personen"},
		asset.New(),
	))
}
