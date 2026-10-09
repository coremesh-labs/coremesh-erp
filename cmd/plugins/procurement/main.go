// Command procurement ist das Plugin der Beschaffung (Modul procurement,
// internal/procurement): Angebote und Eingangsrechnungen von Handwerkern und
// Dienstleistern, gebucht über die Vorerfassung des Hauptbuchs.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/procurement/internal/procurement"
)

const version = "0.4.2"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "procurement", Version: version, Description: "Beschaffung: Angebote und Eingangsrechnungen"},
		procurement.New(),
	))
}
