package ledger

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
)

// Hook ledger.posting: Andere Module prüfen und ergänzen Buchungen und stoßen
// Folgeaktionen an – bei jedem Buchungsweg (LedgerPosting.post, Vorerfassung;
// modify und check auch beim Prüfen/Simulieren):
//
//	modify  vor der Prüfung: ReturnData {request} ersetzt den Buchungsauftrag
//	check   nach der Prüfung des Ledgers, vor dem Schreiben: E bricht ab
//	commit  nach dem Commit: Folgeaktionen; Fehler kommen als Warnung zurück
//
// Data (PostingHookData): {request, draft_id, simulate, fiscal_year,
// posting_period, ledger, result}. check läuft innerhalb der Transaktion des
// Ledgers – Abonnenten sollen dort nur lesen.

// PostingHook ist der Name des Hooks.
const PostingHook = "ledger.posting"

// PostingHookData sind die Daten des Hooks ledger.posting.
type PostingHookData struct {
	Request       ledgerapi.PostRequest `json:"request"`
	DraftID       string                `json:"draft_id,omitempty"`       // aus der Vorerfassung
	Simulate      bool                  `json:"simulate,omitempty"`       // nur prüfen (kein commit)
	FiscalYear    int                   `json:"fiscal_year,omitempty"`    // ab check
	PostingPeriod int                   `json:"posting_period,omitempty"` // ab check
	Ledger        string                `json:"ledger,omitempty"`         // ab check
	Result        *ledgerapi.PostResult `json:"result,omitempty"`         // commit
	Lines         []PostingHookLine     `json:"lines,omitempty"`          // ab check: geprüfte Positionen mit Kontierung (ACDOCA-Spalten)
}

// PostingHookLine: geprüfte Position (Konto, Positionsart, Kontierung nach dem Modul-Mapping).
type PostingHookLine struct {
	Line     int               `json:"line"`
	Account  string            `json:"account"`
	ItemType string            `json:"item_type"`
	Dims     map[string]string `json:"dims,omitempty"`
}

var postingHookDef = hook.Definition{
	Name:        PostingHook,
	Owner:       Name,
	Description: "Buchen eines Belegs im Hauptbuch (Fachmodule, Vorerfassung, Simulation)",
	Phases:      hook.AllPhases,
	OnFailure:   hook.FailBlock,
	Data: `{"request": ledgerapi.PostRequest (source_module, company_code, document_type, posting_date, currency, header_text, reference, items[account, side, amount, assignments, text]),
 "draft_id": Vorerfassung, "simulate": nur prüfen,
 "fiscal_year", "posting_period", "ledger", "lines" [{line, account, item_type, dims}]: ab check,
 "result": {id, document_number, fiscal_year, posting_period} in commit}
modify: ReturnData mit geändertem "request" ersetzt den Auftrag; check: E bricht ab; commit: Fehler werden Warnungen.`,
}

// defineHooks meldet die Hooks des Ledgers an (beim Start).
func (m *Module) defineHooks(ctx context.Context) {
	if err := hook.Define(ctx, m.services, postingHookDef); err != nil {
		m.log.WarnContext(ctx, "Hook nicht angemeldet", "hook", PostingHook, "err", err.Error())
	}
	if err := hook.Define(ctx, m.services, chartChangeHookDef); err != nil {
		m.log.WarnContext(ctx, "Hook nicht angemeldet", "hook", chartChangeHookDef.Name, "err", err.Error())
	}
}

// hookModify ruft modify auf und übernimmt einen geänderten Auftrag.
func (s *PostingService) hookModify(ctx context.Context, d PostingHookData) (ledgerapi.PostRequest, []hook.Message, error) {
	res, err := hook.Call(ctx, s.m.services, PostingHook, hook.PhaseModify, d)
	if err != nil {
		return d.Request, nil, err
	}
	if err := res.Err(); err != nil {
		return d.Request, res.Messages, err
	}
	var out PostingHookData
	if err := res.DecodeData(&out); err != nil {
		return d.Request, res.Messages, fmt.Errorf("Hook %s/modify: Daten unlesbar: %w", PostingHook, err)
	}
	return normalize(out.Request), res.Messages, nil
}

// hookCheck ruft check auf; Meldungen E brechen ab.
func (s *PostingService) hookCheck(ctx context.Context, d PostingHookData) ([]hook.Message, error) {
	res, err := hook.Call(ctx, s.m.services, PostingHook, hook.PhaseCheck, d)
	if err != nil {
		return nil, err
	}
	return res.Messages, res.Err()
}

// hookCommit ruft commit nach dem Speichern auf; Fehler sind Warnungen.
func (m *Module) hookCommit(ctx context.Context, req ledgerapi.PostRequest, draftID string, res *ledgerapi.PostResult) {
	if res.Duplicate {
		return // nichts Neues gebucht
	}
	r, err := hook.Call(ctx, m.services, PostingHook, hook.PhaseCommit, PostingHookData{Request: req, DraftID: draftID,
		FiscalYear: res.FiscalYear, PostingPeriod: res.PostingPeriod, Result: res})
	if err != nil {
		res.Messages = append(res.Messages, hook.Warning("HOOK-COMMIT", "Hook "+PostingHook+"/commit: "+err.Error()))
		return
	}
	res.Messages = append(res.Messages, r.Messages...)
}

// messageText: Warnungen und Hinweise der Hooks für die Meldung der Oberfläche.
func messageText(msgs []hook.Message) string {
	var parts []string
	for _, m := range msgs {
		if m.Type == hook.TypeError || m.Type == hook.TypeWarning {
			parts = append(parts, m.Type+": "+m.String())
		}
	}
	return strings.Join(parts, " · ")
}
