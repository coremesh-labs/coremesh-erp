package ledgerapi

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// DocRefs: Verweise, die an einem gebuchten Beleg fehlen – Fields an allen
// Positionen, PartnerFields nur an Debitoren- und Kreditorenpositionen, Lines an
// einzelnen Positionen (je Feld Spalte des Universal Journals, z. B. invoice_id,
// dimension_custom_1, charge_type).
type DocRefs struct {
	CompanyCode   string
	FiscalYear    int
	Number        string
	Fields        map[string]string
	PartnerFields map[string]string
	Lines         map[int]map[string]string
}

// FillReferences trägt Verweise an gebuchten Belegen nach (ledger 0.35.0,
// JournalReference.fill: nur leere Felder, Feldstatus, Änderungsprotokoll, danach
// über den Ausgleich an die Gegenposten) – einmal je Migration name: Merker in
// table (Spalten name, done_at), erst gesetzt, wenn alle Belege durchgingen.
func FillReferences(ctx context.Context, db module.DB, s module.Services, log *slog.Logger, table, name, reason string, refs []DocRefs) error {
	res, err := db.Query(ctx, `SELECT 1 FROM `+table+` WHERE name = ?`, name)
	if err != nil || len(res.Rows) > 0 {
		return err
	}
	changed, failed := 0, 0
	for _, r := range refs {
		id := fmt.Sprintf("%s|%d|%s", r.CompanyCode, r.FiscalYear, r.Number)
		lines := map[string]map[string]string{}
		for l, fs := range r.Lines {
			lines[fmt.Sprint(l)] = fs
		}
		for _, part := range []struct {
			fields  map[string]string
			lines   map[string]map[string]string
			partner bool
		}{{r.Fields, lines, false}, {r.PartnerFields, nil, true}} {
			if len(part.fields) == 0 && len(part.lines) == 0 {
				continue
			}
			resp, err := s.Call(ctx, "JournalReference", "fill", map[string]any{"id": id,
				"data": map[string]any{"fields": part.fields, "lines": part.lines, "partner_only": part.partner, "change_reason": reason}})
			if err != nil {
				failed++
				log.WarnContext(ctx, "Verweise nicht nachgetragen", "document", id, "err", err.Error())
				continue
			}
			var out struct {
				Changed int `json:"changed"`
			}
			_ = sdk.Decode(resp.Payload, &out)
			changed += out.Changed
		}
	}
	log.InfoContext(ctx, "Verweise an gebuchten Belegen nachgetragen", "migration", name, "fields", changed, "failed", failed)
	if failed > 0 {
		return nil // nächster Prozessstart versucht es erneut
	}
	_, err = db.Exec(ctx, `INSERT INTO `+table+` (name, done_at) VALUES (?, ?)`, name, time.Now().UTC().Format(time.RFC3339))
	return err
}
