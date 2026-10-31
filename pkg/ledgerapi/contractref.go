package ledgerapi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// ContractRef: Vertrag, der an einem gebuchten Beleg fehlt (Beleg über
// Buchungskreis und Belegnummer, Geschäftsjahr wird gesucht, wenn 0).
type ContractRef struct {
	CompanyCode string
	FiscalYear  int
	Number      string
	PostingDate string // entscheidet, wenn die Belegnummer in mehreren Jahren vorkommt
	Contract    string
}

// AddContracts trägt den Vertrag an Positionen ohne Vertrag nach (ledger 0.34.0,
// JournalEntry.addContract mit Änderungsprotokoll) – einmal je Migration name:
// Merker in table (Spalten name, done_at), erst gesetzt, wenn alle Belege
// durchgingen. Belege, die das Hauptbuch nicht kennt, zählen als erledigt.
func AddContracts(ctx context.Context, db module.DB, s module.Services, log *slog.Logger, table, name, reason string, refs []ContractRef) error {
	res, err := db.Query(ctx, `SELECT 1 FROM `+table+` WHERE name = ?`, name)
	if err != nil || len(res.Rows) > 0 {
		return err
	}
	changed, failed := 0, 0
	for _, r := range refs {
		if strings.TrimSpace(r.Contract) == "" || r.Number == "" {
			continue
		}
		year := r.FiscalYear
		if year == 0 {
			// Geschäftsjahr über die Positionen (Filter Belegnummer); gibt es die Nummer
			// in mehreren Jahren, entscheidet das Buchungsdatum.
			l, err := s.Call(ctx, "JournalEntryItem", "list", map[string]any{"query": map[string]any{"company_code_id": r.CompanyCode, "document_number": r.Number}})
			if err != nil {
				failed++
				log.WarnContext(ctx, "Vertrag nicht nachgetragen – Beleg nicht lesbar", "document", r.Number, "err", err.Error())
				continue
			}
			var out struct {
				Items []struct {
					Number string `json:"document_number"`
					Year   int    `json:"fiscal_year"`
					Date   string `json:"posting_date"`
				} `json:"items"`
			}
			_ = sdk.Decode(l.Payload, &out)
			years, dated := map[int]bool{}, map[int]bool{}
			for _, it := range out.Items {
				if it.Number != r.Number {
					continue
				}
				years[it.Year] = true
				if r.PostingDate != "" && strings.HasPrefix(it.Date, r.PostingDate) {
					dated[it.Year] = true
				}
			}
			if len(years) > 1 {
				years = dated
			}
			if len(years) != 1 {
				continue
			}
			for y := range years {
				year = y
			}
		}
		resp, err := s.Call(ctx, "JournalEntry", "addContract", map[string]any{"id": fmt.Sprintf("%s|%d|%s", r.CompanyCode, year, r.Number),
			"data": map[string]any{"new_contract": r.Contract, "change_reason": reason}})
		if err != nil {
			failed++
			log.WarnContext(ctx, "Vertrag nicht nachgetragen", "document", r.Number, "contract", r.Contract, "err", err.Error())
			continue
		}
		var out struct {
			Changed int `json:"changed"`
		}
		_ = sdk.Decode(resp.Payload, &out)
		changed += out.Changed
	}
	log.InfoContext(ctx, "Vertrag an gebuchten Belegen nachgetragen", "migration", name, "positions", changed, "failed", failed)
	if failed > 0 {
		return nil // nächster Prozessstart versucht es erneut
	}
	_, err = db.Exec(ctx, `INSERT INTO `+table+` (name, done_at) VALUES (?, ?)`, name, time.Now().UTC().Format(time.RFC3339))
	return err
}
