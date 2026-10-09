// Command bank ist das Plugin des Bankmoduls (Modul bank, internal/bank):
// Kontoumsätze einlesen (CSV mit Spaltenzuordnung), maschinell oder von Hand
// zuordnen (mit gelernten Regeln) und in der Reihenfolge der Zahlungen im
// Hauptbuch buchen.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/bank/internal/bank"
)

const version = "0.1.1"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "bank", Version: version, Description: "Bank: Kontoumsätze einlesen, zuordnen und buchen"},
		bank.New(),
	))
}
