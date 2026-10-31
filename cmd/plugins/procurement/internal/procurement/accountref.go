package procurement

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

var _ module.Migrator = (*Module)(nil)

// Migrate stellt gespeicherte Sachkontonummern auf die reine Nummer um
// (ledger 0.15.0: 1200 statt SKR25-1200; module.Migrator, einmal je Prozess);
// seit 0.14.0 die Vorschlagswerte der Auftragserteilung.
func (m *Module) Migrate(ctx context.Context) error {
	m.migrateDependentTexts(ctx)
	m.migrateOrderCatalogs(ctx)
	if err := ledgerapi.StripChartPrefix(ctx, m.db, m.services, m.log,
		ledgerapi.AccountColumn{Table: "procurement__cost_category", Column: "account_number"},
		ledgerapi.AccountColumn{Table: "procurement__invoice_item", Column: "account_number"},
	); err != nil {
		return err
	}
	if err := m.migrateContracts(ctx); err != nil {
		return err
	}
	if err := m.migrateChargeTypes(ctx); err != nil {
		return err
	}
	if err := m.migrateInvoiceRefs(ctx); err != nil {
		return err
	}
	return m.migrateChargeRefs(ctx)
}

// migrateContracts (0.19.0): Gebuchte Rechnungen mit Vertrag (Rechnung zum Vertrag,
// Rahmenvertrag – sonst der eine Vertrag aller Positionen) tragen ihn an den
// Positionen ohne Vertrag nach; bis 0.18.0 fehlte er (einmal, Merker procurement__migration).
func (m *Module) migrateContracts(ctx context.Context) error {
	res, err := m.db.Query(ctx, `SELECT i.company_code, i.document_id, COALESCE(NULLIF(i.framework_contract, ''),
		(SELECT MIN(p.contract_id) FROM procurement__invoice_item p WHERE p.company_code = i.company_code AND p.invoice_id = i.invoice_id
		 AND p.is_active = ? HAVING COUNT(DISTINCT NULLIF(p.contract_id, '')) = 1), '')
		FROM procurement__invoice i WHERE i.status = ? AND COALESCE(i.document_id, '') <> ''`, true, invoicePosted)
	if err != nil {
		return err
	}
	var refs []ledgerapi.ContractRef
	for _, r := range res.Rows {
		parts := strings.Split(crud.Str(r[1]), "|") // Buchungskreis|Jahr|Nummer
		if len(parts) != 3 {
			continue
		}
		year, _ := strconv.Atoi(parts[1])
		refs = append(refs, ledgerapi.ContractRef{CompanyCode: parts[0], FiscalYear: year, Number: parts[2], Contract: crud.Str(r[2])})
	}
	return ledgerapi.AddContracts(ctx, m.db, m.services, m.log, "procurement__migration", "contract_refs",
		"Migration procurement 0.19.0: Vertrag der Rechnung", refs)
}

// oldDependentText: Text nachgelagerter Positionen vor procurement 0.12.1 mit Punkt
// („Umsatzsteuer 19 % auf 201.73“).
var oldDependentText = regexp.MustCompile(`^(.* )(\d+(?:\.\d+)?) % auf (-?\d+\.\d+)$`)

// migrateDependentTexts schreibt die Texte nachgelagerter Positionen deutsch
// („Umsatzsteuer 19 % auf 201,73“, wie pricing.Result.Text seit 0.12.1).
func (m *Module) migrateDependentTexts(ctx context.Context) {
	for _, k := range []docKind{invoiceDoc, quoteDoc} {
		res, err := m.db.Query(ctx, `SELECT company_code, `+k.key+`, line_no, item_text FROM `+k.items+` WHERE item_text LIKE ?`, "% auf %.%")
		if err != nil {
			m.log.WarnContext(ctx, "Texte nachgelagerter Positionen nicht umgestellt", "table", k.items, "err", err.Error())
			continue
		}
		for _, row := range res.Rows {
			p := oldDependentText.FindStringSubmatch(crud.Str(row[3]))
			if p == nil {
				continue
			}
			text := p[1] + pricing.FormatDE(p[2]) + " % auf " + pricing.FormatDE(p[3])
			if _, err := m.db.Exec(ctx, `UPDATE `+k.items+` SET item_text = ? WHERE company_code = ? AND `+k.key+` = ? AND line_no = ?`,
				text, row[0], row[1], row[2]); err != nil {
				m.log.WarnContext(ctx, "Text nachgelagerter Position nicht umgestellt", "table", k.items, "err", err.Error())
			}
		}
	}
}

// Kontenplanwechsel des Buchungskreises (Hook ledger.chart_change): gespeicherte
// Sachkonten prüfen (check) und nach der Zuordnung umstellen (commit).
const chartChangeCallback = "ProcurementChartChange"

var chartColumns = []ledgerapi.CompanyAccountColumn{
	{Table: "procurement__cost_category", Column: "account_number"},
	{Table: "procurement__invoice_item", Column: "account_number"},
}

func (m *Module) registerChartChange(r *module.Router) {
	hook.Handle(r, chartChangeCallback, func(ctx context.Context, req hook.Request) (hook.Response, error) {
		return ledgerapi.ChartChangeHandler(m.db, m.log, "Beschaffung", chartColumns...)(ctx, req)
	})
}

// migrateInvoiceRefs (0.20.0): Gebuchte Rechnungen – Rechnungsnummer an allen
// Positionen, Objekt (Rechnung, sonst das eine Objekt der Positionen) und Vertrag an
// der Kreditorenposition; die Zahlungen übernehmen sie über den Ausgleich.
func (m *Module) migrateInvoiceRefs(ctx context.Context) error {
	res, err := m.db.Query(ctx, `SELECT i.company_code, i.invoice_id, i.document_id, COALESCE(i.object_type, ''), COALESCE(i.object_id, ''),
		COALESCE(i.framework_contract, '') FROM procurement__invoice i WHERE i.status = ? AND COALESCE(i.document_id, '') <> ''`, invoicePosted)
	if err != nil {
		return err
	}
	var refs []ledgerapi.DocRefs
	for _, r := range res.Rows {
		parts := strings.Split(crud.Str(r[2]), "|") // Buchungskreis|Jahr|Nummer
		if len(parts) != 3 {
			continue
		}
		year, _ := strconv.Atoi(parts[1])
		cc, typ, obj := crud.Str(r[0]), crud.Str(r[3]), crud.Str(r[4])
		if obj == "" {
			items, err := m.db.Query(ctx, `SELECT DISTINCT object_type, object_id FROM procurement__invoice_item
				WHERE company_code = ? AND invoice_id = ? AND is_active = ? AND COALESCE(object_id, '') <> ''`, cc, crud.Str(r[1]), true)
			if err != nil {
				return err
			}
			if len(items.Rows) == 1 {
				typ, obj = crud.Str(items.Rows[0][0]), crud.Str(items.Rows[0][1])
			}
		}
		partner := map[string]string{}
		if f := m.objectField(ctx, cc, typ); f != "" && obj != "" {
			partner[f] = obj
		}
		if c := crud.Str(r[5]); c != "" {
			partner["rent_contract_id"] = c
		}
		refs = append(refs, ledgerapi.DocRefs{CompanyCode: parts[0], FiscalYear: year, Number: parts[2],
			Fields: map[string]string{"invoice_id": crud.Str(r[1])}, PartnerFields: partner})
	}
	return ledgerapi.FillReferences(ctx, m.db, m.services, m.log, "procurement__migration", "invoice_refs",
		"Migration procurement 0.20.0: Verweise der Rechnung", refs)
}

// migrateChargeRefs (0.20.1): Umlagefähige Positionen gebuchter Rechnungen – Belastung
// an ihr Objekt (wie beim Buchen seit 0.20.0) an der Rechnungsposition und an ihrer
// Buchungszeile. Die Zeile wird über Konto und Betrag gesucht und nur bei genau einem
// Treffer ergänzt.
func (m *Module) migrateChargeRefs(ctx context.Context) error {
	if m.text(ctx, `SELECT name FROM procurement__migration WHERE name = ?`, "charge_refs") != "" {
		return nil
	}
	res, err := m.db.Query(ctx, `SELECT i.company_code, i.invoice_id, h.document_id, i.line_no, i.account_number, COALESCE(i.cost_amount, i.amount),
		COALESCE(NULLIF(i.object_id, ''), h.object_id, ''), CASE WHEN COALESCE(i.object_id, '') <> '' THEN COALESCE(i.object_type, '') ELSE COALESCE(h.object_type, '') END,
		COALESCE(i.charge_type, ''), COALESCE(i.charge_id, '')
		FROM procurement__invoice_item i JOIN procurement__invoice h ON h.company_code = i.company_code AND h.invoice_id = i.invoice_id
		WHERE h.status = ? AND COALESCE(h.document_id, '') <> '' AND i.is_active = ? AND i.dependent_type IS NULL AND i.allocable = ?
		ORDER BY h.document_id, i.line_no`, invoicePosted, true, true)
	if err != nil {
		return err
	}
	type line struct {
		No      int
		Account string
		Amount  int64
	}
	docLines := map[string][]line{}
	byDoc := map[string]*ledgerapi.DocRefs{}
	var order []string
	for _, r := range res.Rows {
		cc, doc, obj, typ := crud.Str(r[0]), crud.Str(r[2]), crud.Str(r[6]), crud.Str(r[7])
		code, id := crud.Str(r[8]), crud.Str(r[9])
		if code == "" {
			if obj == "" {
				continue
			}
			if code = m.objectChargeType(ctx, cc, typ); code == "" {
				continue
			}
			id = obj
		}
		parts := strings.Split(doc, "|") // Buchungskreis|Jahr|Nummer
		if len(parts) != 3 {
			continue
		}
		if _, ok := docLines[doc]; !ok {
			l, err := m.services.Call(ctx, "JournalEntryItem", "list", map[string]any{"query": map[string]any{"company_code_id": parts[0], "document_number": parts[2]}})
			if err != nil {
				return err
			}
			var out struct {
				Items []struct {
					Number string `json:"document_number"`
					Year   int    `json:"fiscal_year"`
					Line   int    `json:"line_item_number"`
					Acc    string `json:"account_number"`
					Amount int64  `json:"amount_local_curr"`
				} `json:"items"`
			}
			if err := sdk.Decode(l.Payload, &out); err != nil {
				return err
			}
			seen := map[int]bool{}
			docLines[doc] = []line{}
			for _, it := range out.Items {
				if it.Number == parts[2] && strconv.Itoa(it.Year) == parts[1] && !seen[it.Line] {
					seen[it.Line] = true
					docLines[doc] = append(docLines[doc], line{it.Line, it.Acc, it.Amount})
				}
			}
		}
		amount := toInt(r[5])
		var hits []int
		for _, l := range docLines[doc] {
			if sameAccount(l.Account, crud.Str(r[4])) && (l.Amount == amount || l.Amount == -amount) {
				hits = append(hits, l.No)
			}
		}
		if len(hits) != 1 {
			m.log.WarnContext(ctx, "Belastung an nicht nachgetragen – Buchungszeile nicht eindeutig", "invoice", crud.Str(r[1]), "line", toInt(r[3]), "hits", len(hits))
			continue
		}
		if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_item SET charge_type = ?, charge_id = ? WHERE company_code = ? AND invoice_id = ? AND line_no = ?`,
			code, id, cc, r[1], r[3]); err != nil {
			return err
		}
		d, ok := byDoc[doc]
		if !ok {
			year, _ := strconv.Atoi(parts[1])
			d = &ledgerapi.DocRefs{CompanyCode: parts[0], FiscalYear: year, Number: parts[2], Lines: map[int]map[string]string{}}
			byDoc[doc] = d
			order = append(order, doc)
		}
		d.Lines[hits[0]] = map[string]string{"charge_type": code, "charge_id": id}
	}
	var refs []ledgerapi.DocRefs
	for _, doc := range order {
		refs = append(refs, *byDoc[doc])
	}
	return ledgerapi.FillReferences(ctx, m.db, m.services, m.log, "procurement__migration", "charge_refs",
		"Migration procurement 0.20.1: Belastung an (umlagefähig: Objekt der Position)", refs)
}
