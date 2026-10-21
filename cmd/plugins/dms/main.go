// Command dms ist das Plugin des Dokumentenmanagements (Modul dms,
// internal/dms): Ablageplan, Ablagefristen, Archive, Eingangsverzeichnisse,
// Workflows, Viewer und die Prüfung von Buchungen und Verträgen.
package main

import (
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/plugin"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/dms/internal/dms"
)

const version = "0.16.2"

func main() {
	plugin.Main(module.NewPlugin(
		module.Info{Name: "dms", Version: version, Description: "Dokumentenmanagement: Ablageplan, Archive, Eingang, Workflows"},
		dms.New(),
	))
}
