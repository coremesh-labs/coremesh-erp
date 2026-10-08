package realestate

import (
	"context"

	"github.com/coremesh-labs/coremesh/pkg/sdk/bpref"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

var _ module.Migrator = (*Module)(nil)

// Migrate stellt Verweise auf Geschäftspartner von GUIDs (partner bis 0.9.0)
// auf BP-Nummern um (module.Migrator, einmal je Prozess).
func (m *Module) Migrate(ctx context.Context) error {
	return bpref.Remap(ctx, m.db, m.services, m.log,
		bpref.Column{Table: "realestate__object_partner", Column: "partner_id"},
	)
}
