// Command process ist das Plugin der Vorgänge (Modul process, internal/process):
// Wiedervermietung, Schadensmeldung, Mieter- und Eigentümeranfragen,
// Eigentümerversammlung … mit Schritten, Fristen, Wiedervorlage, Sichten,
// Hinweisen in der App und einem Ereignisjournal für andere Systeme.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/process/internal/process"
)

const version = "0.3.0"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "process", Version: version, Description: "Vorgänge: Wiedervermietung, Schaden, Anfragen, Eigentümerversammlung …"},
		process.New(),
	))
}
