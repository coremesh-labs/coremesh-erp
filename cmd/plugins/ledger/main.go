// Command ledger ist das Plugin des Hauptbuchs (Modul ledger, internal/ledger):
// Kontenpläne (SKA1/SKB1), Universal Journal (BKPF/ACDOCA), Vorerfassung,
// Periodensperre, Währungen und Kurse. Beschreibung: README.md.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/ledger/internal/ledger"
)

const version = "0.13.0"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "ledger", Version: version, Description: "Hauptbuch (General Ledger): Kontenpläne, Universal Journal, Vorerfassung, Perioden, Währungen"},
		ledger.New(),
	))
}
