// Command techobj ist das Plugin der technischen Objekte (Modul techobj,
// internal/techobj): Zähler, Heizung, Aufzug, Rauchwarnmelder … mit Lagebaum,
// Messpunkten, Versorgungsbaum, Gerätetypen, Nummern, Zählwerken, Einbau und
// Ablesungen – angebunden an andere Module nur über die Brücke.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/techobj/internal/techobj"
)

const version = "0.6.4"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "techobj", Version: version, Description: "Technische Objekte: Zähler, Heizung, Aufzug … mit Lage, Messpunkten und Ablesungen"},
		techobj.New(),
	))
}
