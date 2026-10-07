// Package ledgerapi ist die Schnittstelle des Hauptbuchs (Plugin ledger) für
// buchende Fachmodule wie Vertrieb (SD), Vermietung (RENT) oder Einkauf
// (PROCUREMENT). Module buchen synchron über diesen Client – nie über die
// Tabellen des Plugins:
//
//	gl := ledgerapi.New(env.Services)
//	res, err := gl.Post(ctx, ledgerapi.PostRequest{
//		SourceModule: "RENT", SourceReference: "SOLL-2026-10-MV-0007",
//		CompanyCode: "1000", PostingDate: "2026-10-01", Currency: "EUR",
//		HeaderText: "Sollstellung Miete Oktober",
//		Items: []ledgerapi.Item{
//			{Account: "1200", Side: ledgerapi.Debit, Amount: "1250.00", // Mietforderungen
//				Assignments: map[string]string{"contract": "MV-0007", "object": "WE-0001-0003"}},
//			{Account: "6000", Side: ledgerapi.Credit, Amount: "1000.00", // Sollmiete kalt
//				Assignments: map[string]string{"contract": "MV-0007", "object": "WE-0001-0003"}},
//			{Account: "2800", Side: ledgerapi.Credit, Amount: "250.00", // BK-Vorauszahlung
//				Assignments: map[string]string{"contract": "MV-0007", "object": "WE-0001-0003"}},
//		},
//	})
//
// Assignments sind die Kontierungsobjekte des Moduls mit dessen eigenen
// Namen. In welche Spalten des Universal Journals sie geschrieben werden,
// bestimmt das Modul-Mapping des Buchungskreises (ledger_company_config).
// SourceReference macht die Buchung idempotent: Ein zweiter Aufruf mit
// derselben Referenz liefert den vorhandenen Beleg (Duplicate = true).
package ledgerapi

import (
	"context"
	"github.com/camel/coremesh/pkg/sdk/hook"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/module"
)

// Object und Actions des Buchungsservice im Dispatcher (nur JSON-API, ohne
// Oberfläche). Manuelle Buchungen laufen in der Oberfläche über die
// Vorerfassung (JournalDraft: speichern, dann buchen).
const (
	Object         = "LedgerPosting"
	ActionPost     = "post"
	ActionSimulate = "simulate"
	ActionReverse  = "reverse"
)

// Side ist die Soll/Haben-Kennzeichnung (SHKZG).
type Side string

const (
	Debit  Side = "S" // Soll
	Credit Side = "H" // Haben
)

// Item ist eine Position eines Buchungsbelegs.
type Item struct {
	Account string `json:"account_number"`
	Side    Side   `json:"shkzg"`
	Amount  string `json:"amount"` // positiver Betrag in Belegwährung, Dezimalpunkt
	Text    string `json:"item_text,omitempty"`
	// ItemType: Positionsart (GL, CUSTOMER, SUPPLIER, TAX); leer = aus dem Konto abgeleitet.
	ItemType string `json:"item_type,omitempty"`
	// Allgemeine Kontierungen (direkte ACDOCA-Spalten).
	CostCenter   string `json:"cost_center,omitempty"`
	ProfitCenter string `json:"profit_center,omitempty"`
	Segment      string `json:"segment,omitempty"`
	// Assignments: Kontierungsobjekte des Moduls, z. B. {"contract": "MV-0007"};
	// Zielspalten laut Modul-Mapping des Buchungskreises.
	Assignments map[string]string `json:"assignments,omitempty"`
}

// PostRequest ist ein Buchungsauftrag.
type PostRequest struct {
	SourceModule    string `json:"source_module"`              // SD, RENT, PROCUREMENT, MANUAL …
	SourceReference string `json:"source_reference,omitempty"` // Idempotenzschlüssel des Moduls
	CompanyCode     string `json:"company_code"`
	DocumentType    string `json:"document_type,omitempty"`  // Standard SA
	PostingDate     string `json:"posting_date"`             // JJJJ-MM-TT
	DocumentDate    string `json:"document_date,omitempty"`  // Standard: Buchungsdatum
	PostingPeriod   int    `json:"posting_period,omitempty"` // nur Sonderperioden 13–16 (Dezember)
	Currency        string `json:"currency"`                 // Belegwährung
	HeaderText      string `json:"header_text,omitempty"`
	Reference       string `json:"reference,omitempty"`
	Items           []Item `json:"items"`
}

// PostResult ist der gebuchte (oder bei gleicher SourceReference vorhandene) Beleg.
type PostResult struct {
	ID             string `json:"id"`
	DocumentNumber string `json:"document_number"`
	FiscalYear     int    `json:"fiscal_year"`
	PostingPeriod  int    `json:"posting_period"`
	Duplicate      bool   `json:"duplicate,omitempty"`
	// Messages: Warnungen und Hinweise der Hooks (ledger.posting: modify, check, commit).
	Messages []hook.Message `json:"messages,omitempty"`
}

// ReverseRequest storniert einen Beleg.
type ReverseRequest struct {
	ID          string `json:"id"`
	PostingDate string `json:"posting_date,omitempty"` // Standard heute
	HeaderText  string `json:"header_text,omitempty"`
}

// Client ruft das Hauptbuch über den Dispatcher auf.
type Client struct{ s module.Services }

// New erzeugt den Client (env.Services des aufrufenden Moduls).
func New(services module.Services) Client { return Client{services} }

// Post bucht einen Beleg.
func (c Client) Post(ctx context.Context, req PostRequest) (PostResult, error) {
	var out PostResult
	resp, err := c.s.Call(ctx, Object, ActionPost, map[string]any{"data": req})
	if err != nil {
		return out, err
	}
	return out, sdk.Decode(resp.Payload, &out)
}

// Simulate prüft einen Buchungsauftrag vollständig (Periode, Konten, Mapping,
// Soll = Haben, Umrechnung), ohne zu buchen.
func (c Client) Simulate(ctx context.Context, req PostRequest) error {
	_, err := c.s.Call(ctx, Object, ActionSimulate, map[string]any{"data": req})
	return err
}

// Reverse storniert einen Beleg und liefert den Stornobeleg.
func (c Client) Reverse(ctx context.Context, req ReverseRequest) (PostResult, error) {
	var out PostResult
	resp, err := c.s.Call(ctx, Object, ActionReverse, map[string]any{"id": req.ID,
		"data": map[string]any{"posting_date": req.PostingDate, "header_text": req.HeaderText}})
	if err != nil {
		return out, err
	}
	return out, sdk.Decode(resp.Payload, &out)
}
