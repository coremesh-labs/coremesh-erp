package opcost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Nebenkostenabrechnung (Mieter):
//
//   - Regelwerk (SettlementDefinition) je Abrechnungseinheit (Gebäude bzw.
//     Wirtschaftseinheit), z. B. „Nebenkosten“ und „Haushaltsnahe
//     Dienstleistungen“: Rechengenauigkeit (Faktor), Rundung, Vertragsarten der
//     Mieter, Konditionsarten der Vorauszahlungen, Konten der Buchung.
//   - Regeln (SettlementRule), beliebig viele Schritte, je Regel ein Topf:
//     COLLECT (Quelle → Topf: Sachkonto, Eingangsrechnungen oder
//     Vertragsabrechnungen einer Kostenart), TRANSFER (Topf → Topf, Anteil %),
//     DISTRIBUTE (Topf → Mietobjekte nach Verteilerschlüssel, danach
//     tagesgenau auf Mieter und Leerstand).
//   - Lauf (SettlementRun): contract-billing rechnet (Haskell) und liefert das
//     Journal (doppelte Buchführung auf Abrechnungskonten, mit Formel und
//     Quelle), das Ergebnis je Mieter und die Prüfungen; gespeichert hier.
//     Neu rechnen, solange er nicht freigegeben ist. Freigeben von Hand, dann
//     Buchen: je Mieter Vorauszahlungen an Erlöse, Differenz an das Mieterkonto.

const (
	definitionObject = "SettlementDefinition"
	ruleObject       = "SettlementRule"
	runObject        = "SettlementRun"
	journalObject    = "SettlementJournal"
	tenantObject     = "SettlementTenant"
	serviceObject    = "OpCostService"

	runDraft    = "DRAFT"
	runReleased = "RELEASED"
	runPosted   = "POSTED"
)

// Zuordnung einer Quelle zum Abrechnungszeitraum.
const (
	assignService  = "SERVICE_PERIOD"
	assignPosting  = "POSTING_DATE"
	assignDocument = "DOCUMENT_DATE"
)

var (
	tDate = metamodel.TypeDate
	tArea = metamodel.TypeTextarea

	unitTypeOptions     = []metamodel.Option{{Value: "Building", Label: "Gebäude"}, {Value: "BusinessEntity", Label: "Wirtschaftseinheit"}}
	assignmentOptions   = []metamodel.Option{
		{Value: assignService, Label: "Leistungs- bzw. Abrechnungszeitraum (anteilig nach Tagen)"},
		{Value: assignPosting, Label: "Buchungsdatum"}, {Value: assignDocument, Label: "Belegdatum (Rechnungs- bzw. Abrechnungsdatum)"}}
	roundingOptions     = []metamodel.Option{{Value: "TENANTS_OR_OWNER", Label: "Rest ≥ Anzahl Mieter (Cent): auf Mieter verteilen, sonst Eigentümer"}, {Value: "OWNER", Label: "Rest immer an den Eigentümer"}}
	ruleKindOptions     = []metamodel.Option{{Value: "COLLECT", Label: "Sammeln (Quelle → Topf)"}, {Value: "TRANSFER", Label: "Umbuchen (Topf → Topf, Anteil %)"}, {Value: "DISTRIBUTE", Label: "Verteilen (Topf → Mietobjekte → Mieter)"}}
	sourceTypeOptions   = []metamodel.Option{{Value: "LEDGER", Label: "Sachkonto (Buchungen im Zeitraum)"}, {Value: "INVOICE", Label: "Eingangsrechnungen (Kostenart, Leistungszeitraum)"}, {Value: "CONTRACT_SETTLEMENT", Label: "Vertragsabrechnungen (Kostenart, z. B. WEG)"}}
	runStatusOptions    = []metamodel.Option{{Value: runDraft, Label: "Entwurf (neu rechenbar)"}, {Value: runReleased, Label: "freigegeben"}, {Value: runPosted, Label: "gebucht"}}
	tenantStatusOptions = []metamodel.Option{{Value: "OPEN", Label: "offen"}, {Value: "DRAFT", Label: "vorerfasst"}, {Value: "POSTED", Label: "gebucht"}}
	codeListRe          = regexp.MustCompile(`^[A-Z0-9_]+(,[A-Z0-9_]+)*$`)
	pctRe               = regexp.MustCompile(`^\d{1,3}([.]\d{1,6})?$`)

	lookupDefinition = &metamodel.Lookup{Object: definitionObject, ValueField: "code", LabelFields: []string{"name"}, Filters: map[string]string{"company_code": "company_code"}}
	lookupCategory   = &metamodel.Lookup{Object: "CostCategory", ValueField: "code", LabelFields: []string{"name"}, Filters: map[string]string{"company_code": "company_code"}}
	lookupKey        = &metamodel.Lookup{Object: "AllocationKey", ValueField: "code", LabelFields: []string{"name"}, Filters: map[string]string{"company_code": "company_code"}}
)

func defMatch() map[string]string {
	return map[string]string{"company_code": "company_code", "definition": "code"}
}

func runMatch() map[string]string {
	return map[string]string{"company_code": "company_code", "definition": "definition", "period_from": "period_from"}
}

func (m *Module) definition() *crud.Entity {
	return &crud.Entity{
		Object: definitionObject, Title: "Regelwerke (Nebenkosten)", Icon: "icon-calc", Table: "opcost__definition", Section: "Abrechnung",
		Keys: []string{"company_code", "code"}, Order: "company_code, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code", "unit_id"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung (z. B. Nebenkosten, haushaltsnahe Dienstleistungen)", Type: tText, Required: true, Listable: true},
			{Key: "unit_type", Label: "Abrechnungseinheit (Art)", Type: tSel, Required: true, Listable: true, Options: unitTypeOptions},
			{Key: "unit_id", Label: "Abrechnungseinheit", Type: tText, Required: true, Listable: true},
			{Key: "tenant_contract_types", Label: "Vertragsarten der Mieter (z. B. MV,GM,SP)", Type: tText, Required: true, Group: "Rechnung"},
			{Key: "advance_types", Label: "Konditionsarten der Vorauszahlungen (z. B. NK,HK)", Type: tText, Group: "Rechnung"},
			{Key: "scale", Label: "Rechengenauigkeit (Faktor auf Cent, z. B. 100000)", Type: tNum, Group: "Rechnung"},
			{Key: "rounding_rule", Label: "Rundung", Type: tSel, Group: "Rechnung", Options: roundingOptions},
			{Key: "ledger", Label: "Ledger der Quelle Sachkonto", Type: tText, Group: "Rechnung"},
			{Key: "advance_account", Label: "Konto der Vorauszahlungen (z. B. 2800)", Type: tText, Group: "Buchung", Lookup: lookupAccount},
			{Key: "revenue_account", Label: "Erlöskonto abgerechnete Betriebskosten (z. B. 6100)", Type: tText, Group: "Buchung", Lookup: lookupAccount},
			{Key: "document_type", Label: "Belegart (Nachzahlung)", Type: tText, Group: "Buchung"},
			{Key: "credit_document_type", Label: "Belegart (Guthaben)", Type: tText, Group: "Buchung"},
			{Key: "auto_post", Label: "Automatisch ins Hauptbuch buchen", Type: tBool, Group: "Buchung"},
			{Key: "release_with_errors", Label: "Freigabe trotz Fehlern in den Prüfungen erlauben", Type: tBool, Group: "Buchung"},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "regeln", Title: "Regeln", Relation: &metamodel.Relation{Object: ruleObject, ForeignKey: "definition", Match: defMatch(),
				Columns: []string{"step", "rule_no", "kind", "description", "cost_category", "source_type", "source_value", "pool", "to_pool", "allocation_key"}}},
			{Key: "laeufe", Title: "Abrechnungsläufe", Relation: &metamodel.Relation{Object: runObject, ForeignKey: "definition", Match: defMatch(),
				Columns: []string{"period_from", "period_to", "status", "summary"}}},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
			for k, def := range map[string]any{"scale": 100000, "rounding_rule": "TENANTS_OR_OWNER", "ledger": "0L", "document_type": "DR",
				"credit_document_type": "DG", "auto_post": false, "release_with_errors": false} {
				if rec[k] == nil || crud.Str(rec[k]) == "" {
					rec[k] = def
				}
			}
			if s := toInt(rec["scale"]); s < 1 || s > 1_000_000_000 {
				return crud.Invalid("Rechengenauigkeit: Faktor 1 bis 1.000.000.000")
			}
			for _, k := range []string{"tenant_contract_types", "advance_types"} {
				v := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(crud.Str(rec[k]))), " ", "")
				if v != "" && !codeListRe.MatchString(v) {
					return crud.Invalid("%s: Codes mit Komma, z. B. MV,GM", k)
				}
				rec[k] = nilIfEmpty(v)
			}
			if rec["tenant_contract_types"] == nil {
				return crud.Invalid("Vertragsarten der Mieter sind Pflicht")
			}
			for _, k := range []string{"advance_account", "revenue_account"} {
				if acc := strings.TrimSpace(crud.Str(rec[k])); acc != "" {
					nr, err := m.glAccount(ctx, crud.Str(rec["company_code"]), acc)
					if err != nil {
						return err
					}
					rec[k] = nr
				} else {
					rec[k] = nil
				}
			}
			typ, id := crud.Str(rec["unit_type"]), strings.TrimSpace(crud.Str(rec["unit_id"]))
			if _, err := m.services.Call(ctx, typ, "get", map[string]any{"id": crud.Str(rec["company_code"]) + "|" + id}); err != nil {
				return crud.Invalid("Abrechnungseinheit %s %s gibt es nicht", typ, id)
			}
			rec["unit_id"] = id
			return nil
		},
	}
}

func (m *Module) rule() *crud.Entity {
	return &crud.Entity{
		Object: ruleObject, Title: "Regeln (Nebenkosten)", Icon: "icon-list", Table: "opcost__rule", Section: "Abrechnung",
		Keys: []string{"company_code", "definition", "rule_no"}, Order: "company_code, definition, step, rule_no", StatusField: "is_active",
		Filters: []string{"company_code", "definition", "kind", "pool"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "definition", Label: "Regelwerk", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupDefinition},
			{Key: "rule_no", Label: "Regel", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "step", Label: "Schritt (Reihenfolge)", Type: tNum, Required: true, Listable: true},
			{Key: "description", Label: "Beschreibung", Type: tText, Required: true, Listable: true},
			{Key: "kind", Label: "Art", Type: tSel, Required: true, Listable: true, Options: ruleKindOptions, Trigger: true},
			{Key: "cost_category", Label: "Kostenart (Ausweis)", Type: tText, Listable: true, Lookup: lookupCategory},
			{Key: "source_type", Label: "Quelle", Type: tSel, Options: sourceTypeOptions, Group: "Sammeln",
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{"COLLECT"}}},
			{Key: "source_value", Label: "Sachkonto bzw. Kostenart der Quelle", Type: tText, Group: "Sammeln",
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{"COLLECT"}}},
			{Key: "assignment", Label: "Zuordnung zum Abrechnungszeitraum", Type: tSel, Options: assignmentOptions, Group: "Sammeln",
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{"COLLECT"}}},
			{Key: "pool", Label: "Topf (Ziel beim Sammeln, sonst Quelle)", Type: tText, Required: true, Listable: true},
			{Key: "to_pool", Label: "Ziel-Topf", Type: tText, Group: "Umbuchen", ShowIf: &metamodel.Condition{Field: "kind", Values: []string{"TRANSFER"}}},
			{Key: "share_pct", Label: "Anteil in %", Type: tText, Group: "Umbuchen", ShowIf: &metamodel.Condition{Field: "kind", Values: []string{"TRANSFER"}}},
			{Key: "allocation_key", Label: "Verteilerschlüssel", Type: tText, Group: "Verteilen", Lookup: lookupKey,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{"DISTRIBUTE"}}},
			{Key: "vacancy_to_tenants", Label: "Leerstand nicht abziehen (Kosten nur auf die Mieter)", Type: tBool, Group: "Verteilen",
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{"DISTRIBUTE"}}},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(rule_no), 0) + 10 FROM opcost__rule WHERE company_code = ? AND definition = ?`,
				rec["company_code"], strings.ToUpper(crud.Str(rec["definition"])))
			if err != nil {
				return err
			}
			rec["rule_no"] = toInt(res.Rows[0][0])
			return nil
		},
		Validate: m.checkRule,
	}
}

func (m *Module) checkRule(ctx context.Context, rec, _ crud.Record) error {
	cc := crud.Str(rec["company_code"])
	rec["definition"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["definition"])))
	up := func(k string) string {
		v := strings.ToUpper(strings.TrimSpace(crud.Str(rec[k])))
		rec[k] = nilIfEmpty(v)
		return v
	}
	pool := up("pool")
	if pool == "" {
		return crud.Invalid("Topf ist Pflicht")
	}
	if c := up("cost_category"); c != "" {
		if !m.exists(ctx, "opcost__cost_category", cc, c) {
			return crud.Invalid("Kostenart %q gibt es nicht", c)
		}
	}
	switch crud.Str(rec["kind"]) {
	case "COLLECT":
		src, val := crud.Str(rec["source_type"]), strings.TrimSpace(crud.Str(rec["source_value"]))
		switch src {
		case "LEDGER":
			nr, err := m.glAccount(ctx, cc, val)
			if err != nil {
				return err
			}
			rec["source_value"] = nr
		case "INVOICE", "CONTRACT_SETTLEMENT":
			v := strings.ToUpper(val)
			if !m.exists(ctx, "opcost__cost_category", cc, v) {
				return crud.Invalid("Kostenart %q der Quelle gibt es nicht", v)
			}
			rec["source_value"] = v
		default:
			return crud.Invalid("Sammeln: Quelle ist Pflicht")
		}
		// Einzelposten im Hauptbuch haben keinen Leistungszeitraum: Vorschlag Buchungsdatum, sonst Leistungszeitraum.
		a := crud.Str(rec["assignment"])
		switch {
		case a == "" && src == "LEDGER":
			a = assignPosting
		case a == "":
			a = assignService
		case a == assignService && src == "LEDGER":
			return crud.Invalid("Quelle Sachkonto: Einzelposten haben keinen Leistungszeitraum – Buchungs- oder Belegdatum wählen")
		case a != assignService && a != assignPosting && a != assignDocument:
			return crud.Invalid("Zuordnung: Leistungszeitraum, Buchungsdatum oder Belegdatum")
		}
		rec["assignment"] = a
		rec["to_pool"], rec["share_pct"], rec["allocation_key"] = nil, nil, nil
	case "TRANSFER":
		if up("to_pool") == "" || crud.Str(rec["to_pool"]) == pool {
			return crud.Invalid("Umbuchen: Ziel-Topf (anderer Topf) ist Pflicht")
		}
		pct := strings.ReplaceAll(strings.TrimSpace(crud.Str(rec["share_pct"])), ",", ".")
		if !pctRe.MatchString(pct) {
			return crud.Invalid("Anteil in %%, z. B. 40 oder 12,5")
		}
		rec["share_pct"] = pct
		rec["source_type"], rec["source_value"], rec["allocation_key"], rec["assignment"] = nil, nil, nil, nil
	case "DISTRIBUTE":
		k := up("allocation_key")
		if k == "" || !m.exists(ctx, "opcost__allocation_key", cc, k) {
			return crud.Invalid("Verteilen: Verteilerschlüssel ist Pflicht (Verteilerschlüssel)")
		}
		rec["source_type"], rec["source_value"], rec["to_pool"], rec["share_pct"], rec["assignment"] = nil, nil, nil, nil, nil
	default:
		return crud.Invalid("Art: COLLECT, TRANSFER oder DISTRIBUTE")
	}
	return nil
}

func (m *Module) exists(ctx context.Context, table, cc, code string) bool {
	res, err := m.db.Query(ctx, "SELECT 1 FROM "+table+" WHERE company_code = ? AND code = ?", cc, code)
	return err == nil && len(res.Rows) > 0
}

func (m *Module) run() *crud.Entity {
	return &crud.Entity{
		Object: runObject, Title: "Abrechnungsläufe (Nebenkosten)", Icon: "icon-calc", Table: "opcost__run", Section: "Abrechnung",
		Keys: []string{"company_code", "definition", "period_from"}, Order: "company_code, definition, period_from DESC", TitleField: "summary",
		Filters: []string{"company_code", "definition", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "definition", Label: "Regelwerk", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupDefinition},
			{Key: "period_from", Label: "Abrechnungszeitraum von", Type: tDate, Required: true, Listable: true, Immutable: true},
			{Key: "period_to", Label: "Abrechnungszeitraum bis", Type: tDate, Required: true, Listable: true},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: runStatusOptions},
			{Key: "summary", Label: "Ergebnis", Type: tText, Listable: true, ReadOnly: true},
			{Key: "check_result", Label: "Prüfungen", Type: tArea, ReadOnly: true},
			{Key: "failures", Label: "Fehler in den Prüfungen", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "computed_at", Label: "Gerechnet am", Type: tText, ReadOnly: true},
			{Key: "released_at", Label: "Freigegeben am", Type: tText, ReadOnly: true},
			{Key: "released_by", Label: "Freigegeben von", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "mieter", Title: "Ergebnis je Mieter", Relation: &metamodel.Relation{Object: tenantObject, ForeignKey: "definition", Match: runMatch(),
				Columns: []string{"contract_id", "partner_id", "rent_object_id", "usage_from", "usage_to", "costs", "advances", "balance", "status", "document_number"}}},
			{Key: "journal", Title: "Rechenweg (Journal)", Collapsed: true, Relation: &metamodel.Relation{Object: journalObject, ForeignKey: "definition", Match: runMatch(),
				Columns: []string{"line_no", "step", "rule_no", "debit_account", "credit_account", "amount", "formula", "source_ref"}}},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old != nil && crud.Str(old["status"]) != runDraft {
				return crud.Invalid("Lauf ist freigegeben und nicht mehr änderbar")
			}
			rec["definition"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["definition"])))
			for _, k := range []string{"period_from", "period_to"} {
				d, err := crud.ParseDate(rec[k])
				if err != nil {
					return err
				}
				rec[k] = d
			}
			if crud.Str(rec["period_to"]) < crud.Str(rec["period_from"]) {
				return crud.Invalid("Zeitraum: bis liegt vor von")
			}
			if old == nil {
				rec["status"] = runDraft
			}
			return nil
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "compute", Label: "Rechnen", Record: true}, Handle: m.computeAction},
			{ActionConfig: metamodel.ActionConfig{Name: "release", Label: "Freigeben", Record: true,
				Confirm: "Abrechnung freigeben? Danach ist sie unveränderlich und kann gebucht werden."}, Handle: m.releaseAction},
			{ActionConfig: metamodel.ActionConfig{Name: "post", Label: "Buchen …", Record: true,
				Confirm: "Freigegebene Abrechnung je Mieter im Hauptbuch vorerfassen (bei automatischer Buchung: buchen)?"}, Handle: m.postAction},
		},
	}
}

func (m *Module) journal() *crud.Entity {
	return &crud.Entity{
		Object: journalObject, Title: "Rechenweg (Journal)", Icon: "icon-list", Table: "opcost__journal", Section: "Abrechnung",
		Keys: []string{"company_code", "definition", "period_from", "line_no"}, Order: "company_code, definition, period_from, line_no", ReadOnly: true,
		Filters: []string{"company_code", "definition", "period_from", "step", "debit_account", "credit_account", "cost_category"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Listable: true},
			{Key: "definition", Label: "Regelwerk", Type: tText, Listable: true},
			{Key: "period_from", Label: "Lauf (von)", Type: tDate},
			{Key: "line_no", Label: "Zeile", Type: tNum, Listable: true},
			{Key: "step", Label: "Schritt", Type: tNum, Listable: true},
			{Key: "rule_no", Label: "Regel", Type: tNum, Listable: true},
			{Key: "debit_account", Label: "Soll (Abrechnungskonto)", Type: tText, Listable: true},
			{Key: "credit_account", Label: "Haben (Abrechnungskonto)", Type: tText, Listable: true},
			{Key: "amount", Label: "Betrag", Type: tText, Listable: true},
			{Key: "formula", Label: "Formel", Type: tText, Listable: true},
			{Key: "source_ref", Label: "Quelle", Type: tText, Listable: true},
			{Key: "cost_category", Label: "Kostenart", Type: tText},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			scale := toInt(m.text(ctx, `SELECT scale FROM opcost__run WHERE company_code = ? AND definition = ? AND period_from = ?`,
				rec["company_code"], rec["definition"], rec["period_from"]))
			rec["amount"] = scaledText(toInt(rec["amount"]), scale)
			return nil
		},
	}
}

func (m *Module) tenant() *crud.Entity {
	return &crud.Entity{
		Object: tenantObject, Title: "Ergebnis je Mieter", Icon: "icon-users", Table: "opcost__tenant", Section: "Abrechnung",
		Keys: []string{"company_code", "definition", "period_from", "contract_id"}, Order: "company_code, definition, period_from, contract_id", ReadOnly: true,
		Filters: []string{"company_code", "definition", "period_from", "contract_id", "partner_id", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Listable: true},
			{Key: "definition", Label: "Regelwerk", Type: tText, Listable: true},
			{Key: "period_from", Label: "Lauf (von)", Type: tDate},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Listable: true},
			{Key: "partner_id", Label: "Mieter", Type: tText, Listable: true,
				Lookup: &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"search_term", "name1"}}},
			{Key: "rent_object_id", Label: "Mietobjekt", Type: tText, Listable: true},
			{Key: "usage_from", Label: "Nutzung von", Type: tDate, Listable: true},
			{Key: "usage_to", Label: "Nutzung bis", Type: tDate, Listable: true},
			{Key: "costs", Label: "Kosten", Type: tText, Listable: true},
			{Key: "advances", Label: "Vorauszahlungen", Type: tText, Listable: true},
			{Key: "balance", Label: "Nachzahlung (+) / Guthaben (−)", Type: tText, Listable: true},
			{Key: "lines", Label: "Kosten je Kostenart", Type: tArea},
			{Key: "status", Label: "Buchung", Type: tSel, Listable: true, Options: tenantStatusOptions},
			{Key: "draft_id", Label: "Vorerfassung", Type: tText},
			{Key: "document_number", Label: "Beleg", Type: tText, Listable: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Decorate: func(_ context.Context, rec crud.Record) error {
			for _, k := range []string{"costs", "advances", "balance"} {
				rec[k] = scaledText(toInt(rec[k]), 1)
			}
			return nil
		},
	}
}

// scaledText: Cent × Faktor → Euro als Dezimaltext (alle Stellen des Faktors).
func scaledText(v, scale int64) string {
	if scale < 1 {
		scale = 1
	}
	r := new(big.Rat).SetFrac(big.NewInt(v), big.NewInt(100*scale))
	digits := 2
	for s := scale; s >= 10; s /= 10 {
		digits++
	}
	return r.FloatString(digits)
}

// --- Actions --------------------------------------------------------------------------

type runKey struct {
	CC, Definition, From string
}

func (m *Module) runKeyOf(payload any) (runKey, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(payload, &in); err != nil {
		return runKey{}, err
	}
	key, err := m.set.Entity(runObject).ParseID(in.ID)
	if err != nil {
		return runKey{}, err
	}
	from, err := crud.ParseDate(key["period_from"])
	return runKey{CC: crud.Str(key["company_code"]), Definition: crud.Str(key["definition"]), From: from}, err
}

func (m *Module) runStatus(ctx context.Context, k runKey) (status, to string, err error) {
	res, err := m.db.Query(ctx, `SELECT status, period_to FROM opcost__run WHERE company_code = ? AND definition = ? AND period_from = ?`, k.CC, k.Definition, k.From)
	if err != nil {
		return "", "", err
	}
	if len(res.Rows) == 0 {
		return "", "", fmt.Errorf("%w: Abrechnungslauf %s ab %s", sdk.ErrNotFound, k.Definition, k.From)
	}
	to, _ = crud.ParseDate(res.Rows[0][1])
	return crud.Str(res.Rows[0][0]), to, nil
}

func requireAccess(ctx context.Context, object, action, cc string) error {
	ok, err := sdk.CheckAccess(ctx, object, action, cc)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: keine Berechtigung für %s.%s im Buchungskreis %s", sdk.ErrPermissionDenied, object, action, cc)
	}
	return nil
}

// computeAction: „Rechnen“ – Regelwerk, Regeln und Schlüssel an contract-billing,
// Journal, Ergebnis je Mieter und Prüfungen zurück und hier speichern.
func (m *Module) computeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	k, err := m.runKeyOf(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireAccess(ctx, runObject, "compute", k.CC); err != nil {
		return sdk.Response{}, err
	}
	status, to, err := m.runStatus(ctx, k)
	if err != nil {
		return sdk.Response{}, err
	}
	if status != runDraft {
		return sdk.Response{}, crud.Invalid("Lauf ist freigegeben – nicht neu rechenbar")
	}
	def, err := m.set.Entity(definitionObject).Load(ctx, crud.Record{"company_code": k.CC, "code": k.Definition})
	if err != nil {
		return sdk.Response{}, err
	}
	rules, err := m.query(ctx, `SELECT rule_no, step, description, kind, cost_category, source_type, source_value, pool, to_pool, share_pct,
		allocation_key, vacancy_to_tenants, assignment FROM opcost__rule WHERE company_code = ? AND definition = ? AND is_active = ? ORDER BY step, rule_no`,
		[]string{"rule_no", "step", "description", "kind", "cost_category", "source_type", "source_value", "pool", "to_pool", "share_pct",
			"allocation_key", "vacancy_to_tenants", "assignment"}, k.CC, k.Definition, true)
	if err != nil {
		return sdk.Response{}, err
	}
	keys, err := m.query(ctx, `SELECT code, name, basis, measurement_type FROM opcost__allocation_key WHERE company_code = ?`,
		[]string{"code", "name", "basis", "measurement_type"}, k.CC)
	if err != nil {
		return sdk.Response{}, err
	}
	categories, err := m.query(ctx, `SELECT code, name FROM opcost__cost_category WHERE company_code = ?`, []string{"code", "name"}, k.CC)
	if err != nil {
		return sdk.Response{}, err
	}
	resp, err := m.services.Call(ctx, "ContractBilling", "computeOpCost", map[string]any{"company_code": k.CC, "definition": def,
		"rules": rules, "keys": keys, "categories": categories, "period_from": k.From, "period_to": to})
	if errors.Is(err, sdk.ErrUnimplemented) {
		return sdk.Response{}, fmt.Errorf("%w: Rechnen nicht verfügbar – Plugin contract-billing ist nicht gestartet", sdk.ErrUnavailable)
	}
	if err != nil {
		return sdk.Response{}, err
	}
	var out struct {
		Summary  string `json:"summary"`
		Checks   string `json:"check_result"`
		Failures int64  `json:"failures"`
		Journal []struct {
			Step         int64  `json:"step"`
			Rule         int64  `json:"rule"`
			Debit        string `json:"debit"`
			Credit       string `json:"credit"`
			Amount       int64  `json:"amount"`
			Formula      string `json:"formula"`
			Source       string `json:"source"`
			CostCategory string `json:"cost_category"`
		} `json:"journal"`
		Tenants []struct {
			Contract  string `json:"contract_id"`
			Partner   string `json:"partner_id"`
			Object    string `json:"rent_object_id"`
			UsageFrom string `json:"usage_from"`
			UsageTo   string `json:"usage_to"`
			Costs     int64  `json:"costs"`
			Advances  int64  `json:"advances"`
			Lines     string `json:"lines"`
		} `json:"tenants"`
		Message string `json:"message"`
		Table   any    `json:"table"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return sdk.Response{}, err
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, t := range []string{"opcost__journal", "opcost__tenant"} {
			if _, err := m.db.Exec(ctx, "DELETE FROM "+t+" WHERE company_code = ? AND definition = ? AND period_from = ?", k.CC, k.Definition, k.From); err != nil {
				return err
			}
		}
		for i, j := range out.Journal {
			if _, err := m.db.Exec(ctx, `INSERT INTO opcost__journal (company_code, definition, period_from, line_no, step, rule_no, debit_account,
				credit_account, amount, formula, source_ref, cost_category) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, k.CC, k.Definition, k.From,
				i+1, j.Step, j.Rule, j.Debit, j.Credit, j.Amount, nilIfEmpty(j.Formula), nilIfEmpty(j.Source), nilIfEmpty(j.CostCategory)); err != nil {
				return err
			}
		}
		for _, t := range out.Tenants {
			if _, err := m.db.Exec(ctx, `INSERT INTO opcost__tenant (company_code, definition, period_from, contract_id, partner_id, rent_object_id, usage_from,
				usage_to, costs, advances, balance, lines, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'OPEN')`, k.CC, k.Definition, k.From, t.Contract,
				t.Partner, nilIfEmpty(t.Object), t.UsageFrom, t.UsageTo, t.Costs, t.Advances, t.Costs-t.Advances, nilIfEmpty(t.Lines)); err != nil {
				return err
			}
		}
		_, err := m.db.Exec(ctx, `UPDATE opcost__run SET summary = ?, check_result = ?, failures = ?, computed_at = ?, scale = ? WHERE company_code = ?
			AND definition = ? AND period_from = ?`, out.Summary, nilIfEmpty(out.Checks), out.Failures, time.Now().UTC().Format(time.RFC3339), toInt(def["scale"]), k.CC, k.Definition, k.From)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": out.Message, "table": out.Table}}, nil
}

// query: Zeilen als Datensätze (Spaltennamen).
func (m *Module) query(ctx context.Context, sql string, cols []string, args ...any) ([]map[string]any, error) {
	res, err := m.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range res.Rows {
		rec := map[string]any{}
		for i, c := range cols {
			rec[c] = r[i]
		}
		out = append(out, rec)
	}
	return out, nil
}

func (m *Module) text(ctx context.Context, sql string, args ...any) string {
	res, err := m.db.Query(ctx, sql, args...)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	return crud.Str(res.Rows[0][0])
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		var i int64
		fmt.Sscan(strings.TrimSpace(n), &i)
		return i
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// releaseAction: „Freigeben“ – nur ein gerechneter Entwurf.
func (m *Module) releaseAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	k, err := m.runKeyOf(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireAccess(ctx, runObject, "release", k.CC); err != nil {
		return sdk.Response{}, err
	}
	if m.text(ctx, `SELECT computed_at FROM opcost__run WHERE company_code = ? AND definition = ? AND period_from = ?`, k.CC, k.Definition, k.From) == "" {
		return sdk.Response{}, crud.Invalid("Lauf ist noch nicht gerechnet")
	}
	failures := toInt(m.text(ctx, `SELECT failures FROM opcost__run WHERE company_code = ? AND definition = ? AND period_from = ?`, k.CC, k.Definition, k.From))
	if failures > 0 && !crud.AsBool(m.text(ctx, `SELECT release_with_errors FROM opcost__definition WHERE company_code = ? AND code = ?`, k.CC, k.Definition)) {
		return sdk.Response{}, crud.Invalid("Prüfung mit %d Fehlern – nicht freigebbar (siehe Prüfungen; Regelwerk: „Freigabe trotz Fehlern“)", failures)
	}
	res, err := m.db.Exec(ctx, `UPDATE opcost__run SET status = ?, released_at = ?, released_by = ? WHERE company_code = ? AND definition = ?
		AND period_from = ? AND status = ?`, runReleased, time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID),
		k.CC, k.Definition, k.From, runDraft)
	if err != nil {
		return sdk.Response{}, err
	}
	if res.RowsAffected == 0 {
		return sdk.Response{}, crud.Invalid("Lauf ist nicht im Entwurf")
	}
	return sdk.Response{Payload: map[string]any{"message": "Abrechnung freigegeben"}}, nil
}
