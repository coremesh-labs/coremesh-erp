// Command inspection ist das Plugin der Begehungen und Protokolle (Modul
// inspection, internal/inspection): Wohnungsübergabe bei Ein- und Auszug,
// Begehungen – mit Räumen und Bauteilen aus dem Raumbuch, Zählerständen aus den
// technischen Objekten, Teilnehmern, Prüfliste, Druck mit Unterschriften, Ablage
// im DMS und Mängeln als Vorgang.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/inspection/internal/inspection"
)

const version = "0.1.1"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "inspection", Version: version, Description: "Begehungen und Protokolle: Wohnungsübergabe, Begehung mit Räumen, Zählerständen und Unterschriften"},
		inspection.New(),
	))
}
