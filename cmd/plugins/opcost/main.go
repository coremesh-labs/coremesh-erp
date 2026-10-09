// Command opcost ist das Plugin der Betriebskosten (Modul opcost,
// internal/opcost): Kostenarten und Verteilerschlüssel – Grundlage für
// Vertragsabrechnungen, Eingangsrechnungen und den Nebenkostenrechner.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/opcost/internal/opcost"
)

const version = "0.3.3"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "opcost", Version: version, Description: "Betriebskosten: Kostenarten und Verteilerschlüssel"},
		opcost.New(),
	))
}
