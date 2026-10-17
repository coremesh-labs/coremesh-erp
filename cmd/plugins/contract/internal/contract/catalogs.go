package contract

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
)

// Kataloge je Buchungskreis: Vertragsarten, Konditionsarten (Verhalten),
// Konditionsgegenstände (Leistung), Partnerrollen (Aktivierung aus dem
// Partnermodul) und Kontenfindung (Vertragsart × Gegenstand × Konditionsart).

const (
	dirReceivable = "RECEIVABLE"
	dirPayable    = "PAYABLE"
	claimMain     = "MAIN"
	claimSecond   = "SECONDARY"

	// Abrechnungsweg der Vertragsart (seit 0.26.0)
	billPosting = "POSTING" // Sollstellung
	billInvoice = "INVOICE" // Eingangsrechnung: Konditionen sind Vorlagen
	billBoth    = "BOTH"    // je Konditionsart

	// Abrechnungsform der Konditionsart (seit 0.26.0)
	formClaim   = "CLAIM"   // Forderung / Entgelt
	formAdvance = "ADVANCE" // Vorauszahlung, wird abgerechnet
	formPartial = "PARTIAL" // Abschlag, wird mit der Schlussrechnung verrechnet
	formInvoice = "INVOICE" // Rechnungsposition: keine Sollstellung
)

var (
	codeRe = regexp.MustCompile(`^[A-Z0-9_]{1,12}$`)

	directionOptions = []metamodel.Option{
		{Value: dirReceivable, Label: "Forderung (wir erhalten)", Short: "Ford."}, {Value: dirPayable, Label: "Verbindlichkeit (wir zahlen)", Short: "Verb."}}
	claimOptions = []metamodel.Option{
		{Value: claimMain, Label: "Hauptforderung"}, {Value: claimSecond, Label: "Nebenforderung (Kosten, Zinsen, Gebühren)"}}
	billingModeOptions = []metamodel.Option{
		{Value: billPosting, Label: "Sollstellung", Short: "Soll"},
		{Value: billInvoice, Label: "Eingangsrechnung (Konditionen sind Vorlagen, keine Sollstellung)", Short: "Rechnung"},
		{Value: billBoth, Label: "Beides (je Konditionsart, z. B. Abschläge und Schlussrechnung)", Short: "beides"}}
	billingFormOptions = []metamodel.Option{
		{Value: formClaim, Label: "Forderung / Entgelt (Sollstellung)", Short: "Forderung"},
		{Value: formAdvance, Label: "Vorauszahlung (Sollstellung, wird abgerechnet)", Short: "Vorauszahlung"},
		{Value: formPartial, Label: "Abschlag (Sollstellung, mit der Schlussrechnung verrechnet)", Short: "Abschlag"},
		{Value: formInvoice, Label: "Rechnungsposition (keine Sollstellung, Vorlage für Eingangsrechnungen)", Short: "Rechnung"}}
	objectTypeOptions = []metamodel.Option{
		{Value: "RentObject", Label: "Mietobjekt"}, {Value: "Building", Label: "Gebäude"}, {Value: "BusinessEntity", Label: "Wirtschaftseinheit", Short: "WE"}}
)

func catalogFields(extra ...crud.Field) []crud.Field {
	fs := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
		{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
	}
	fs = append(fs, extra...)
	return append(fs,
		crud.Field{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
		crud.Field{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true})
}

func (m *Module) catalog(object, title, table string, fields []crud.Field, check func(context.Context, crud.Record) error) *crud.Entity {
	return &crud.Entity{
		Object: object, Title: title, Icon: "icon-tag", Table: table, Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: fields,
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, object, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = trimUpper(rec["code"])
				if !codeRe.MatchString(crud.Str(rec["code"])) {
					return crud.Invalid("Schlüssel %q: A–Z, 0–9, _ (höchstens 12 Zeichen)", crud.Str(rec["code"]))
				}
			}
			return check(ctx, rec)
		},
	}
}

// --- Vertragsarten -------------------------------------------------------------------

func (m *Module) contractType() *crud.Entity {
	e := m.catalog("ContractType", "Vertragsarten", "contract__contract_type", catalogFields(
		crud.Field{Key: "direction", Label: "Richtung", Type: tSel, Required: true, Listable: true, Options: directionOptions},
		crud.Field{Key: "main_role", Label: "Rolle des Vertragspartners", Type: tText, Required: true, Listable: true,
			Lookup: &metamodel.Lookup{Object: "ContractPartnerRole", ValueField: "role_code", LabelFields: []string{"name"},
				Filters: map[string]string{"company_code": "company_code"}}},
		crud.Field{Key: "range_key", Label: "Nummernkreis (Intervallschlüssel; leer = Schlüssel der Vertragsart)", Type: tText, Listable: true},
		crud.Field{Key: "needs_object", Label: "Objekt nötig", Type: tBool, Listable: true},
		crud.Field{Key: "object_types", Label: "Erlaubte Objekte (RentObject, Building, BusinessEntity; leer = alle)", Type: tText},
		crud.Field{Key: "exclusive_objects", Label: "Objekt exklusiv (ein Vertrag je Stichtag)", Type: tBool, Listable: true},
		crud.Field{Key: "parent_types", Label: "Bezugsvertrag Pflicht – erlaubte Vertragsarten (z. B. MV,GM; leer = kein Bezug)", Type: tText},
		crud.Field{Key: "partner_account_required", Label: "Partnerkonto im Buchungskreis Pflicht (Buchungskreisdaten der Rolle mit Abstimmkonto)",
			Type: tBool, Listable: true, Group: "Buchung"},
		crud.Field{Key: "posting_document_type", Label: "Belegart der Sollstellung (leer = DR bzw. KR)", Type: tText, Group: "Buchung",
			Lookup: &metamodel.Lookup{Object: "DocumentType", ValueField: "code", LabelFields: []string{"name"}}},
		crud.Field{Key: "credit_document_type", Label: "Belegart für Gutschriften (leer = DG bzw. KG)", Type: tText, Group: "Buchung",
			Lookup: &metamodel.Lookup{Object: "DocumentType", ValueField: "code", LabelFields: []string{"name"}}},
		crud.Field{Key: "settlement_tolerance", Label: "Toleranz der Abrechnungsprüfung (Betrag, z. B. 0,05)", Type: tText, Group: "Abrechnung"},
		crud.Field{Key: "auto_post", Label: "Automatisch ins Hauptbuch buchen (sonst bleibt die geprüfte Vorerfassung offen)", Type: tBool, Group: "Buchung"},
		crud.Field{Key: "without_conditions", Label: "Aktivieren ohne Konditionen erlaubt", Type: tBool},
		crud.Field{Key: "billing_mode", Label: "Abrechnungsweg", Type: tSel, Listable: true, Options: billingModeOptions, Group: "Rechnungen"},
		crud.Field{Key: "template_invoices", Label: "Eingangsrechnungen aus Vorlagen (aus dem Abrechnungsweg)", Type: tBool, ReadOnly: true, Group: "Rechnungen"},
		crud.Field{Key: "invoice_type", Label: "Rechnungsart der Eingangsrechnungen zum Vertrag (leer = ER)", Type: tText, Group: "Rechnungen",
			Lookup: &metamodel.Lookup{Object: "InvoiceType", ValueField: "code", LabelFields: []string{"name"}, Filters: map[string]string{"company_code": "company_code"}}},
		crud.Field{Key: "default_tax_code", Label: "Standard-Steuerkennzeichen der Rechnungen", Type: tText, Group: "Rechnungen",
			Lookup: &metamodel.Lookup{Object: "TaxCode", ValueField: "code", LabelFields: []string{"name"}}},
	), m.checkContractType)
	e.Events, e.CompanyCodeField = true, "company_code" // contract-billing hält eine Kopie
	e.Actions = []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "setup", Label: "Buchungskreis einrichten …",
		Fields: []string{"company_code"}}, Handle: m.setupCompanyAction}}
	return e
}

func (m *Module) checkContractType(ctx context.Context, rec crud.Record) error {
	defaults(rec, map[string]any{"needs_object": false, "exclusive_objects": false, "sort_order": 0, "auto_post": false, "partner_account_required": true,
		"without_conditions":   false,
		"settlement_tolerance": "0.05"})
	if err := m.checkBillingMode(ctx, rec); err != nil {
		return err
	}
	rec["invoice_type"] = nilIfEmpty(trimUpper(rec["invoice_type"]))
	rec["default_tax_code"] = nilIfEmpty(trimUpper(rec["default_tax_code"]))
	tol := strings.ReplaceAll(strings.TrimSpace(crud.Str(rec["settlement_tolerance"])), ",", ".")
	if !amountRe.MatchString(tol) || strings.HasPrefix(tol, "-") {
		return crud.Invalid("Toleranz der Abrechnungsprüfung: Betrag ≥ 0, z. B. 0,05")
	}
	rec["settlement_tolerance"] = tol
	if crud.Str(rec["range_key"]) == "" {
		rec["range_key"] = rec["code"]
	}
	rec["range_key"] = trimUpper(rec["range_key"])
	if !codeRe.MatchString(crud.Str(rec["range_key"])) {
		return crud.Invalid("Nummernkreis %q: A–Z, 0–9, _ (höchstens 12 Zeichen)", crud.Str(rec["range_key"]))
	}
	types, err := parseObjectTypes(crud.Str(rec["object_types"]))
	if err != nil {
		return err
	}
	rec["object_types"] = nilIfEmpty(strings.Join(types, ","))
	rec["parent_types"] = nilIfEmpty(strings.Join(codeList(crud.Str(rec["parent_types"])), ","))
	rec["main_role"] = trimUpper(rec["main_role"])
	if _, err := m.roleSetting(ctx, crud.Str(rec["company_code"]), crud.Str(rec["main_role"])); err != nil {
		return err
	}
	if crud.AsBool(rec["partner_account_required"]) {
		return m.requireFinanceRole(ctx, crud.Str(rec["main_role"]))
	}
	return nil
}

// codeList: "mv, gm;SP" → [MV GM SP].
func codeList(text string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if t = strings.ToUpper(t); !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// parseObjectTypes: "rentobject, Building" → [RentObject Building]; leer = alle.
func parseObjectTypes(text string) ([]string, error) {
	var out []string
	for _, t := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		i := slices.IndexFunc(objectTypeOptions, func(o metamodel.Option) bool { return strings.EqualFold(o.Value, t) })
		if i < 0 {
			return nil, crud.Invalid("Objekt %q – erlaubt: RentObject, Building, BusinessEntity", t)
		}
		if v := objectTypeOptions[i].Value; !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out, nil
}

// typeRow: Vertragsart.
type typeRow struct {
	Code, Name, Direction, MainRole, RangeKey string
	ObjectTypes                               []string
	NeedsObject, Exclusive, AccountRequired   bool
	WithoutConditions                         bool     // Aktivieren ohne Konditionen erlaubt
	ParentTypes                               []string // Bezugsvertrag Pflicht, erlaubte Vertragsarten
	BillingMode                               string   // Abrechnungsweg (POSTING, INVOICE, BOTH)
}

// TemplateInvoices: Eingangsrechnungen aus Vorlagen (Abrechnungsweg Eingangsrechnung oder beides).
func (t *typeRow) TemplateInvoices() bool {
	return t.BillingMode == billInvoice || t.BillingMode == billBoth
}

func (m *Module) contractTypeOf(ctx context.Context, cc, code string) (*typeRow, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, direction, main_role, range_key, object_types, needs_object, exclusive_objects,
		partner_account_required, parent_types, without_conditions, COALESCE(billing_mode, '') FROM contract__contract_type WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Vertragsart %q gibt es im Buchungskreis %s nicht (oder inaktiv)", code, cc)
	}
	r := res.Rows[0]
	types, _ := parseObjectTypes(crud.Str(r[5]))
	return &typeRow{Code: crud.Str(r[0]), Name: crud.Str(r[1]), Direction: crud.Str(r[2]), MainRole: crud.Str(r[3]), RangeKey: crud.Str(r[4]),
		ObjectTypes: types, NeedsObject: crud.AsBool(r[6]), Exclusive: crud.AsBool(r[7]), AccountRequired: crud.AsBool(r[8]),
		ParentTypes: codeList(crud.Str(r[9])), WithoutConditions: crud.AsBool(r[10]), BillingMode: firstNonEmpty(crud.Str(r[11]), billPosting)}, nil
}

// checkBillingMode: Abrechnungsweg (leer = Sollstellung); „aus Vorlagen“ folgt ihm.
// Sollstellung schließt Konditionen mit einer Konditionsart „Rechnungsposition“ aus.
func (m *Module) checkBillingMode(ctx context.Context, rec crud.Record) error {
	mode := trimUpper(rec["billing_mode"])
	if mode == "" {
		mode = billPosting
	}
	if !slices.ContainsFunc(billingModeOptions, func(o metamodel.Option) bool { return o.Value == mode }) {
		return crud.Invalid("Abrechnungsweg %q – erlaubt: %s, %s, %s", mode, billPosting, billInvoice, billBoth)
	}
	rec["billing_mode"], rec["template_invoices"] = mode, mode != billPosting
	if mode != billPosting || crud.Str(rec["code"]) == "" {
		return nil
	}
	res, err := m.db.Query(ctx, `SELECT DISTINCT k.contract_id FROM contract__condition k JOIN contract__condition_kind d
		ON d.company_code = k.company_code AND d.code = k.condition_kind
		WHERE k.company_code = ? AND k.contract_type = ? AND d.billing_form = ? ORDER BY k.contract_id LIMIT 5`,
		rec["company_code"], rec["code"], formInvoice)
	if err != nil {
		return err
	}
	if len(res.Rows) > 0 {
		var ids []string
		for _, r := range res.Rows {
			ids = append(ids, crud.Str(r[0]))
		}
		return crud.Invalid("Sollstellung: Verträge dieser Art haben Rechnungspositionen (%s) – Konditionsart dort ändern oder „Beides“ wählen",
			strings.Join(ids, ", "))
	}
	return nil
}

// --- Konditionsarten (Verhalten, seit 0.26.0) -----------------------------------------

func (m *Module) conditionKind() *crud.Entity {
	e := m.catalog("ContractConditionKind", "Konditionsarten", "contract__condition_kind", catalogFields(
		crud.Field{Key: "billing_form", Label: "Abrechnungsform", Type: tSel, Required: true, Listable: true, Options: billingFormOptions},
		crud.Field{Key: "claim_class", Label: "Forderungsklasse", Type: tSel, Required: true, Listable: true, Options: claimOptions},
		crud.Field{Key: "clearing_order", Label: "Verrechnungsreihenfolge (klein = zuerst)", Type: tNum, Listable: true},
		crud.Field{Key: "installments", Label: "Monatsraten bei einmaligen Konditionen (z. B. Kaution: 3)", Type: tNum},
	), func(ctx context.Context, rec crud.Record) error {
		defaults(rec, map[string]any{"sort_order": 0, "installments": 1})
		if n := toInt(rec["installments"]); n < 1 || n > 60 {
			return crud.Invalid("Monatsraten 1 bis 60")
		}
		if rec["clearing_order"] == nil || crud.Str(rec["clearing_order"]) == "" {
			rec["clearing_order"] = map[string]int{claimMain: 30, claimSecond: 10}[crud.Str(rec["claim_class"])]
		}
		if crud.Str(rec["billing_form"]) != formInvoice {
			return nil
		}
		res, err := m.db.Query(ctx, `SELECT DISTINCT k.contract_id FROM contract__condition k JOIN contract__contract_type t
			ON t.company_code = k.company_code AND t.code = k.contract_type
			WHERE k.company_code = ? AND k.condition_kind = ? AND COALESCE(t.billing_mode, ?) = ? ORDER BY k.contract_id LIMIT 5`,
			rec["company_code"], rec["code"], billPosting, billPosting)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			var ids []string
			for _, r := range res.Rows {
				ids = append(ids, crud.Str(r[0]))
			}
			return crud.Invalid("Rechnungsposition: Konditionen mit dieser Art stehen in Verträgen mit Sollstellung (%s)", strings.Join(ids, ", "))
		}
		return nil
	})
	e.AfterUpdate = func(ctx context.Context, rec crud.Record) error {
		return m.syncLegacyKind(ctx, crud.Str(rec["company_code"]), "default_kind", crud.Str(rec["code"]))
	}
	e.Events, e.CompanyCodeField = true, "company_code" // contract-billing: is_template der Konditionen
	return e
}

// kindRow: Konditionsart.
type kindRow struct {
	Code, Name, Form, Claim string
	Installments            int64
}

func (m *Module) conditionKindOf(ctx context.Context, cc, code string) (*kindRow, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, billing_form, claim_class, installments FROM contract__condition_kind
		WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Konditionsart %q gibt es im Buchungskreis %s nicht (oder inaktiv)", code, cc)
	}
	r := res.Rows[0]
	return &kindRow{Code: crud.Str(r[0]), Name: crud.Str(r[1]), Form: crud.Str(r[2]), Claim: crud.Str(r[3]), Installments: toInt(r[4])}, nil
}

// --- Konditionsgegenstände (bis 0.25.0 „Konditionsarten“) ---------------------------

func (m *Module) conditionType() *crud.Entity {
	e := m.catalog("ConditionType", "Konditionsgegenstände", "contract__condition_type", catalogFields(
		crud.Field{Key: "default_kind", Label: "Standard-Konditionsart (neue Konditionen)", Type: tText, Required: true, Listable: true,
			Lookup: kindLookup},
		crud.Field{Key: "tax_code", Label: "Steuerkennzeichen", Type: tText, Listable: true,
			Lookup: &metamodel.Lookup{Object: "TaxCode", ValueField: "code", LabelFields: []string{"name"}}},
		crud.Field{Key: "rounding_rule", Label: "Rundungsregel (leer = Buchungskreis)", Type: tText, Lookup: lookupRounding},
		// bis 0.25.0 an der Konditionsart gepflegt; folgt der Standard-Konditionsart (ältere Leser)
		crud.Field{Key: "claim_class", Label: "Forderungsklasse (aus der Standard-Konditionsart)", Type: tSel, ReadOnly: true, Options: claimOptions},
	), func(ctx context.Context, rec crud.Record) error {
		rec["rounding_rule"] = nilIfEmpty(trimUpper(rec["rounding_rule"]))
		rec["tax_code"] = nilIfEmpty(trimUpper(rec["tax_code"]))
		defaults(rec, map[string]any{"sort_order": 0})
		rec["default_kind"] = trimUpper(rec["default_kind"])
		k, err := m.conditionKindOf(ctx, crud.Str(rec["company_code"]), crud.Str(rec["default_kind"]))
		if err != nil {
			return err
		}
		rec["claim_class"] = k.Claim
		return nil
	})
	sync := func(ctx context.Context, rec crud.Record) error {
		return m.syncLegacyKind(ctx, crud.Str(rec["company_code"]), "code", crud.Str(rec["code"]))
	}
	e.AfterCreate, e.AfterUpdate = sync, sync
	return e
}

// kindLookup: Auswahl der Konditionsarten des Buchungskreises.
var kindLookup = &metamodel.Lookup{Object: "ContractConditionKind", ValueField: "code", LabelFields: []string{"name"},
	Filters: map[string]string{"company_code": "company_code"}}

// syncLegacyKind: Verhaltensspalten der Gegenstände (bis 0.25.0) folgen der Standard-Konditionsart;
// col = code (ein Gegenstand) bzw. default_kind (alle Gegenstände einer Konditionsart).
func (m *Module) syncLegacyKind(ctx context.Context, cc, col, value string) error {
	_, err := m.db.Exec(ctx, `UPDATE contract__condition_type SET (claim_class, is_advance, is_template, clearing_order, installments) =
		(SELECT d.claim_class, d.billing_form = ?, d.billing_form = ?, d.clearing_order, d.installments FROM contract__condition_kind d
		 WHERE d.company_code = contract__condition_type.company_code AND d.code = contract__condition_type.default_kind)
		WHERE company_code = ? AND `+col+` = ? AND default_kind IS NOT NULL`, formAdvance, formInvoice, cc, value)
	return err
}

// --- Nachgelagerte Konditionsarten (seit 0.22.0) ------------------------------------------

var (
	dependentMethods = []metamodel.Option{{Value: pricing.MethodTax, Label: "Steuer (Satz aus dem Steuerkennzeichen)", Short: "Steuer"},
		{Value: pricing.MethodPercent, Label: "Prozent der Basis", Short: "Prozent"}}
	dependentEffects = []metamodel.Option{{Value: pricing.EffectSurcharge, Label: "Zuschlag (kommt zur Basis hinzu, z. B. Umsatzsteuer)", Short: "Zuschlag"},
		{Value: pricing.EffectShare, Label: "Anteil (aus der Basis herausgerechnet, z. B. Materialanteil)", Short: "Anteil"}}
	lookupRounding = &metamodel.Lookup{Object: "RoundingRule", ValueField: "code", LabelFields: []string{"name"}}
)

func (m *Module) dependentType() *crud.Entity {
	return m.catalog("ContractDependentType", "Nachgelagerte Konditionsarten", "contract__dependent_type", catalogFields(
		crud.Field{Key: "method", Label: "Berechnung", Type: tSel, Required: true, Listable: true, Options: dependentMethods, Trigger: true},
		crud.Field{Key: "tax_code", Label: "Steuerkennzeichen (leer = das des Gegenstands der Basis)", Type: tText, Listable: true,
			Lookup: &metamodel.Lookup{Object: "TaxCode", ValueField: "code", LabelFields: []string{"name"}},
			ShowIf: &metamodel.Condition{Field: "method", Values: []string{pricing.MethodTax}}},
		crud.Field{Key: "percent", Label: "Prozentsatz (z. B. 10)", Type: tText, Listable: true,
			ShowIf: &metamodel.Condition{Field: "method", Values: []string{pricing.MethodPercent}}},
		crud.Field{Key: "effect", Label: "Wirkung", Type: tSel, Required: true, Listable: true, Options: dependentEffects},
		crud.Field{Key: "base_types", Label: "Basis: Konditionsgegenstände (leer = alle außer Steuer)", Type: tText, Listable: true},
		crud.Field{Key: "account_number", Label: "Sachkonto (leer = Kontenfindung bzw. Steuerkonto)", Type: tText},
		crud.Field{Key: "rounding_rule", Label: "Rundungsregel (leer = Buchungskreis)", Type: tText, Lookup: lookupRounding},
		crud.Field{Key: "invoice_default", Label: "Standard für Rechnungen und Angebote ohne Vertrag (Beschaffung)", Type: tBool, Listable: true},
	), func(ctx context.Context, rec crud.Record) error {
		defaults(rec, map[string]any{"sort_order": 0, "invoice_default": false})
		if crud.Str(rec["effect"]) == "" {
			rec["effect"] = pricing.EffectSurcharge
		}
		switch crud.Str(rec["method"]) {
		case pricing.MethodTax:
			rec["percent"] = nil
			if code := trimUpper(rec["tax_code"]); code != "" {
				if _, _, _, err := pricing.New(m.services).TaxRate(ctx, code); err != nil {
					return crud.Invalid("%v", err)
				}
				rec["tax_code"] = code
			} else {
				rec["tax_code"] = nil
			}
		case pricing.MethodPercent:
			rec["tax_code"] = nil
			p, err := pricing.ParseDecimal(crud.Str(rec["percent"]))
			if err != nil || p.Sign() < 0 || p.Cmp(big.NewRat(1000, 1)) > 0 {
				return crud.Invalid("Prozentsatz %q: Zahl von 0 bis 1000", crud.Str(rec["percent"]))
			}
			rec["percent"] = pricing.FormatTrim(p, 6)
		default:
			return crud.Invalid("Berechnung: Steuer oder Prozent")
		}
		var base []string
		for _, b := range strings.Split(crud.Str(rec["base_types"]), ",") {
			if b = trimUpper(b); b != "" {
				if _, err := m.conditionTypeOf(ctx, crud.Str(rec["company_code"]), b); err != nil {
					return err
				}
				base = append(base, b)
			}
		}
		rec["base_types"] = nilIfEmpty(strings.Join(base, ","))
		rec["rounding_rule"] = nilIfEmpty(trimUpper(rec["rounding_rule"]))
		rec["account_number"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["account_number"])))
		return nil
	})
}

// --- Partnerrollen -------------------------------------------------------------------

func (m *Module) partnerRole() *crud.Entity {
	return &crud.Entity{
		Object: "ContractPartnerRole", Title: "Partnerrollen", Icon: "icon-users", Table: "contract__partner_role", Section: "Einstellungen",
		Keys: []string{"company_code", "role_code"}, Order: "company_code, sort_order, role_code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"role_code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "role_code", Label: "Rolle (aus dem Partnermodul)", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "PartnerRoleType", ValueField: "code", LabelFields: []string{"description"}}},
			{Key: "name", Label: "Bezeichnung (leer = aus dem Partnermodul)", Type: tText, Listable: true},
			{Key: "is_exclusive", Label: "Exklusiv (ein Partner je Stichtag)", Type: tBool, Listable: true},
			{Key: "with_share", Label: "Mit Anteil in % (Summe ≤ 100)", Type: tBool, Listable: true},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "ContractPartnerRole", action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			defaults(rec, map[string]any{"is_exclusive": false, "with_share": false, "sort_order": 0})
			if old == nil {
				rec["role_code"] = trimUpper(rec["role_code"])
			}
			types, err := m.partnerRoleTypes(ctx)
			if err != nil {
				return err
			}
			desc, ok := types[crud.Str(rec["role_code"])]
			if !ok {
				return crud.Invalid("Rolle %s gibt es im Partnermodul nicht (Geschäftspartner → Kataloge → Rollentypen)", crud.Str(rec["role_code"]))
			}
			if strings.TrimSpace(crud.Str(rec["name"])) == "" {
				rec["name"] = desc
			}
			return nil
		},
	}
}

// roleSettingRow: aktivierte Rolle.
type roleSettingRow struct {
	Code, Name        string
	Exclusive, Shares bool
}

func (m *Module) roleSetting(ctx context.Context, cc, code string) (*roleSettingRow, error) {
	res, err := m.db.Query(ctx, `SELECT role_code, name, is_exclusive, with_share FROM contract__partner_role
		WHERE company_code = ? AND role_code = ? AND is_active = ?`, cc, code, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Rolle %s ist im Buchungskreis %s nicht aktiviert (Verträge → Einstellungen → Partnerrollen)", code, cc)
	}
	r := res.Rows[0]
	return &roleSettingRow{Code: crud.Str(r[0]), Name: crud.Str(r[1]), Exclusive: crud.AsBool(r[2]), Shares: crud.AsBool(r[3])}, nil
}

// partnerRoleTypes: heute gültige Rollentypen des Partnermoduls (Code → Text).
func (m *Module) partnerRoleTypes(ctx context.Context) (map[string]string, error) {
	resp, err := m.services.Call(ctx, "PartnerRoleType", "list", map[string]any{"query": map[string]any{}})
	if err != nil {
		return nil, unavailable("Partnermodul", err)
	}
	var out struct {
		Items []struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	types := map[string]string{}
	for _, it := range out.Items {
		types[it.Code] = it.Description
	}
	return types, nil
}

func unavailable(what string, err error) error {
	if errors.Is(err, sdk.ErrUnimplemented) || errors.Is(err, sdk.ErrUnavailable) {
		return crud.Invalid("%s nicht verfügbar: %v", what, err)
	}
	return err
}

// --- Kontenfindung -------------------------------------------------------------------

func (m *Module) account() *crud.Entity {
	return &crud.Entity{
		Object: "ContractAccount", Title: "Kontenfindung", Icon: "icon-book", Table: "contract__account", Section: "Einstellungen",
		Keys:  []string{"company_code", "contract_type", "condition_type", "account_number"},
		Order: "company_code, contract_type, condition_type, account_number", StatusField: "is_active", TitleField: "account_name",
		Filters: []string{"company_code", "contract_type", "condition_type", "condition_kind"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_type", Label: "Vertragsart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ContractType")},
			{Key: "condition_type", Label: "Konditionsgegenstand", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ConditionType")},
			{Key: "condition_kind", Label: "Konditionsart (leer = alle)", Type: tText, Listable: true, Lookup: kindLookup},
			{Key: "account_number", Label: "Sachkonto", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "GLAccountCompany", ValueField: "account_number", LabelFields: []string{"account_name"},
					Filters: map[string]string{"company_code_id": "company_code", "is_blocked": "=false"}}},
			{Key: "account_name", Label: "Kontobezeichnung", Type: tText, Listable: true, ReadOnly: true},
			{Key: "is_default", Label: "Standard", Type: tBool, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "ContractAccount", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkAccount,
	}
}

func (m *Module) checkAccount(ctx context.Context, rec, old crud.Record) error {
	defaults(rec, map[string]any{"is_default": false})
	cc := crud.Str(rec["company_code"])
	if old == nil {
		rec["contract_type"], rec["condition_type"] = trimUpper(rec["contract_type"]), trimUpper(rec["condition_type"])
		rec["account_number"] = strings.TrimSpace(crud.Str(rec["account_number"]))
		if _, err := m.contractTypeOf(ctx, cc, crud.Str(rec["contract_type"])); err != nil {
			return err
		}
		if _, err := m.conditionTypeOf(ctx, cc, crud.Str(rec["condition_type"])); err != nil {
			return err
		}
	}
	rec["condition_kind"] = nilIfEmpty(trimUpper(rec["condition_kind"]))
	if k := crud.Str(rec["condition_kind"]); k != "" {
		if _, err := m.conditionKindOf(ctx, cc, k); err != nil {
			return err
		}
	}
	nr, name, err := m.glAccount(ctx, cc, crud.Str(rec["account_number"]))
	if err != nil {
		return err
	}
	if old == nil {
		rec["account_number"] = nr // wie im Hauptbuch geführt (Schlüssel)
	}
	rec["account_name"] = nilIfEmpty(name)
	// Höchstens ein Standardkonto je Vertragsart, Gegenstand und Konditionsart.
	if crud.AsBool(rec["is_default"]) {
		_, err := m.db.Exec(ctx, `UPDATE contract__account SET is_default = ? WHERE company_code = ? AND contract_type = ? AND condition_type = ?
			AND COALESCE(condition_kind, '') = ? AND account_number <> ?`,
			false, cc, rec["contract_type"], rec["condition_type"], crud.Str(rec["condition_kind"]), rec["account_number"])
		return err
	}
	return nil
}

// glAccount: Sachkonto im Buchungskreis (Hauptbuch), nicht gesperrt; liefert die Bezeichnung.
// glAccount prüft ein Sachkonto im Buchungskreis und liefert seine Nummer, wie
// das Hauptbuch sie führt (reine Nummer, z. B. 6000), und die Bezeichnung.
// Eingaben mit Kontenplan-Präfix (SKR25-6000) passen zur Nummer dahinter.
func (m *Module) glAccount(ctx context.Context, cc, number string) (string, string, error) {
	resp, err := m.services.Call(ctx, "GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": cc, "account_number": number}})
	if err != nil {
		return "", "", unavailable("Hauptbuch", err)
	}
	var out struct {
		Items []struct {
			AccountNumber string `json:"account_number"`
			AccountName   string `json:"account_name"`
			IsBlocked     bool   `json:"is_blocked"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return "", "", err
	}
	for _, it := range out.Items {
		if sameAccount(it.AccountNumber, number) {
			if it.IsBlocked {
				return "", "", crud.Invalid("Sachkonto %s ist im Buchungskreis %s gesperrt", number, cc)
			}
			return it.AccountNumber, it.AccountName, nil
		}
	}
	return "", "", crud.Invalid("Sachkonto %s gibt es im Buchungskreis %s nicht – steht es im Kontenplan: Sachkonten (Kontenplan) → Konto → „Im Buchungskreis aktivieren …“", number, cc)
}

// sameAccount: gleiche Nummer, mit oder ohne Kontenplan-Präfix (SKR25-6000 = 6000).
func sameAccount(a, b string) bool {
	a, b = strings.ToUpper(strings.TrimSpace(a)), strings.ToUpper(strings.TrimSpace(b))
	return a == b || strings.HasSuffix(a, "-"+b) || strings.HasSuffix(b, "-"+a)
}

// --- Vorschlagswerte und Einrichtung ---------------------------------------------------

// defaultRoles: Rollen, die die Einrichtung aktiviert – sofern das Partnermodul
// sie kennt (Code, exklusiv, Anteil).
var defaultRoles = []struct {
	code              string
	exclusive, shares bool
}{
	{"TENANT", false, false}, {"LANDLORD", false, false}, {"OWNER", false, true}, {"CREDITOR", false, false},
	{"DEBITOR", false, false}, {"WEGADM", true, false}, {"JANITOR", true, false},
	{"GUARANTOR", false, false}, {"PAYER", true, false}, {"AUTHORITY", false, false},
}

var defaultTypes = []struct {
	code, name, direction, role, objects string
	needsObject, exclusive               bool
	parents, credit                      string // Bezugsvertragsarten, Belegart bei negativem Saldo
}{
	{"MV", "Wohnraummiete", dirReceivable, "TENANT", "RentObject", true, true, "", ""},
	{"GM", "Gewerbemiete", dirReceivable, "TENANT", "RentObject", true, true, "", ""},
	{"SP", "Stellplatzmiete", dirReceivable, "TENANT", "RentObject", true, true, "", ""},
	{"HG", "Hausgeld (WEG)", dirReceivable, "OWNER", "RentObject", true, false, "", ""},
	{"VV", "WEG-Verwaltervertrag", dirPayable, "WEGADM", "BusinessEntity", true, false, "", ""},
	{"DL", "Dienstleistung / Wartung", dirPayable, "CREDITOR", "", false, false, "", ""},
	{"VS", "Versicherung", dirPayable, "CREDITOR", "", false, false, "", ""},
	{"VE", "Versorgung (Strom, Wasser, Gas)", dirPayable, "CREDITOR", "", false, false, "", ""},
	{"SO", "Sonstiger Vertrag", dirPayable, "CREDITOR", "", false, false, "", ""},
	{"AM", "Anmietung (Weitervermietung)", dirPayable, "CREDITOR", "RentObject", true, false, "", ""}, // z. B. Stellplatz, der weitervermietet wird
	{"KT", "Mietkaution", dirReceivable, "TENANT", "RentObject", false, false, "MV,GM,SP", ""},
	{"DA", "Darlehen (aufgenommen)", dirPayable, "CREDITOR", "", false, false, "", "KR"}, // Auszahlung ist keine Gutschrift
	{"DV", "Darlehen (vergeben)", dirReceivable, "DEBITOR", "", false, false, "", "DR"},
	{"WH", "Hausgeld an WEG (als Eigentümer)", dirPayable, "CREDITOR", "RentObject", true, false, "", ""},
	{"KV", "Kaufvertrag Immobilie (Abschreibung)", dirPayable, "CREDITOR", "RentObject,Building,BusinessEntity", true, false, "", ""},
	{"GS", "Grundsteuer", dirPayable, "AUTHORITY", "", false, false, "", ""}, // an die Behörde (Rolle Behörde / Amt)
	{"BK", "Bankkonto", dirPayable, "CREDITOR", "", false, false, "", ""},
	{"KK", "Kreditkarte", dirPayable, "CREDITOR", "", false, false, "BK", ""},
	{"RV", "Rahmenvertrag", dirPayable, "CREDITOR", "", false, false, "", ""}, // Eingangsrechnungen aus Vorlagen
}

// billingModeDefault: Abrechnungsweg der Vertragsarten (Vorschlag; sonst Sollstellung).
var billingModeDefault = map[string]string{"RV": billInvoice}

// withoutConditionsDefault: Vertragsarten, die ohne Konditionen aktiviert werden
// dürfen (Vorschlag; an der Vertragsart einstellbar).
var withoutConditionsDefault = map[string]bool{"WH": true}

// noPartnerAccountDefault: Vertragsarten ohne Partnerkonto (Vorschlag) – der
// Kaufvertrag bucht nur die Abschreibung auf Sachkonten.
var noPartnerAccountDefault = map[string]bool{"KV": true}

// defaultKinds: Konditionsarten (seit 0.26.0) – Verhalten bei der Abrechnung.
var defaultKinds = []struct {
	code, name, form, claim string
	order, installments     int
}{
	{"FO", "Forderung / Entgelt", formClaim, claimMain, 30, 1},
	{"VO", "Vorauszahlung (wird abgerechnet)", formAdvance, claimMain, 30, 1},
	{"AB", "Abschlag (mit der Schlussrechnung verrechnet)", formPartial, claimMain, 30, 1},
	{"RA", "Einmalig in Monatsraten (z. B. Kaution)", formClaim, claimMain, 30, 3},
	{"KO", "Nebenforderung Kosten / Gebühren", formClaim, claimSecond, 10, 1},
	{"ZS", "Nebenforderung Zinsen", formClaim, claimSecond, 20, 1},
	{"RP", "Rechnungsposition (Eingangsrechnung, keine Sollstellung)", formInvoice, claimMain, 30, 1},
}

// defaultConditions: Konditionsgegenstände mit Standard-Konditionsart.
var defaultConditions = []struct {
	code, name, kind string
}{
	{"KM", "Kaltmiete", "FO"},
	{"NK", "Betriebskosten-Vorauszahlung", "VO"},
	{"HK", "Heizkosten-Vorauszahlung", "VO"},
	{"ST", "Stellplatzmiete", "FO"},
	{"HG", "Hausgeld", "VO"},
	{"EN", "Entgelt / Prämie", "FO"},
	{"MA", "Material (Pauschale)", "FO"},                               // z. B. Materialanteil am Hausmeistervertrag
	{"NA", "Ergebnis Abrechnung Vorjahr (Nachzahlung/Guthaben)", "FO"}, // einmalig; Guthaben negativ
	{"PE", "Privatentnahme", "FO"},                                     // z. B. monatlicher Übertrag auf ein privates Konto
	// Gerätemiete eines Messdienstes (Heizkosten-/Wasserabrechnung)
	{"MH", "Miete Heizkostenverteiler", "FO"},
	{"MK", "Miete Kaltwasserzähler", "FO"},
	{"MW", "Miete Warmwasserzähler", "FO"},
	{"MZ", "Miete Wärmezähler", "FO"},
	{"RW", "Wartung Rauchwarnmelder", "FO"},
	{"RT", "Rechnung nach Vorlage (Rahmenvertrag)", "RP"},
	{"MR", "Miete Rauchwarnmelder", "FO"}, // nicht umlagefähig (BGH VIII ZR 379/20)
	// Abrechnungsdienst eines Messdienstes je Kostenart (Heizkosten-/Wasserabrechnung)
	{"AH", "Abrechnungsdienst Heizung", "FO"},
	{"AW", "Abrechnungsdienst Warmwasser", "FO"},
	{"AK", "Abrechnungsdienst Kaltwasser", "FO"},
	{"MM", "Mietminderung", "FO"},
	{"BG", "Bereitstellungsgebühr", "FO"},
	{"MG", "Mahngebühr", "KO"},
	{"ZI", "Verzugszinsen", "ZS"},
	{"KA", "Mietkaution", "RA"},
	{"DZ", "Darlehenszinsen", "FO"},
	{"DT", "Tilgung", "FO"},
	{"DS", "Sondertilgung", "FO"},
	{"AZ", "Darlehensauszahlung", "FO"},
	{"HV", "Hausgeld-Vorauszahlung (an WEG)", "VO"},
	{"RZ", "Vorauszahlung Erhaltungsrücklage (an WEG)", "VO"},
	{"VZ", "Vorauszahlung Betriebskosten (Versorger)", "VO"},
	{"GV", "Grundsteuer-Vorauszahlung", "VO"},
	{"KF", "Kontoführungsentgelt", "FO"},
	{"KJ", "Kartengebühr", "FO"},
	{"AF", "Abschreibung (AfA)", "FO"},
	{"UST", "Umsatzsteuer", "FO"}, // Konditionsart der nachgelagerten Umsatzsteuer
}

// defaultDependents: nachgelagerte Konditionsarten (seit 0.22.0) – Umsatzsteuer mit dem
// Steuerkennzeichen der Konditionsart der Basis; Standard für Rechnungen ohne Vertrag.
var defaultDependents = []struct {
	code, name, method, effect string
	invoiceDefault             bool
	sort                       int
}{
	{"UST", "Umsatzsteuer", pricing.MethodTax, pricing.EffectSurcharge, true, 90},
}

// seedKinds legt fehlende Konditionsarten (Vorschlag) an.
func (m *Module) seedKinds(ctx context.Context, cc string) error {
	for i, k := range defaultKinds {
		res, err := m.db.Query(ctx, `SELECT 1 FROM contract__condition_kind WHERE company_code = ? AND code = ?`, cc, k.code)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO contract__condition_kind (company_code, code, name, billing_form, claim_class, clearing_order,
			installments, sort_order, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, k.code, k.name, k.form, k.claim, k.order, k.installments, (i+1)*10, true); err != nil {
			return err
		}
	}
	return nil
}

// setupCompany legt fehlende Vorschlagswerte an: Partnerrollen (soweit im
// Partnermodul vorhanden), Vertragsarten (deren Rolle aktiviert ist),
// Konditionsarten und nachgelagerte Konditionsarten (Umsatzsteuer).
func (m *Module) setupCompany(ctx context.Context, cc string) (roles, types, conds int, missing []string, err error) {
	known, err := m.partnerRoleTypes(ctx)
	if err != nil {
		known = map[string]string{}
	}
	has := func(table, keyCol, key string) (bool, error) {
		res, err := m.db.Query(ctx, "SELECT 1 FROM "+table+" WHERE company_code = ? AND "+keyCol+" = ?", cc, key)
		return err == nil && len(res.Rows) > 0, err
	}
	for i, r := range defaultRoles {
		desc, ok := known[r.code]
		if !ok {
			continue
		}
		if ok, err := has("contract__partner_role", "role_code", r.code); err != nil || ok {
			if err != nil {
				return roles, types, conds, missing, err
			}
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO contract__partner_role (company_code, role_code, name, is_exclusive, with_share, sort_order, is_active)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, cc, r.code, desc, r.exclusive, r.shares, (i+1)*10, true); err != nil {
			return roles, types, conds, missing, err
		}
		roles++
	}
	for i, t := range defaultTypes {
		if ok, err := has("contract__partner_role", "role_code", t.role); err != nil || !ok {
			if err != nil {
				return roles, types, conds, missing, err
			}
			missing = append(missing, t.code+" ("+t.role+")")
			continue // Rolle fehlt im Partnermodul: Vertragsart später selbst anlegen
		}
		if ok, err := has("contract__contract_type", "code", t.code); err != nil || ok {
			if err != nil {
				return roles, types, conds, missing, err
			}
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO contract__contract_type (company_code, code, name, direction, main_role, range_key, needs_object,
			object_types, exclusive_objects, parent_types, credit_document_type, sort_order, is_active, without_conditions, partner_account_required,
			template_invoices, billing_mode) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cc, t.code, t.name, t.direction, t.role, t.code, t.needsObject, nilIfEmpty(t.objects), t.exclusive, nilIfEmpty(t.parents),
			nilIfEmpty(t.credit), (i+1)*10, true, withoutConditionsDefault[t.code], !noPartnerAccountDefault[t.code],
			billingModeDefault[t.code] != "", firstNonEmpty(billingModeDefault[t.code], billPosting)); err != nil {
			return roles, types, conds, missing, err
		}
		types++
	}
	if err := m.seedKinds(ctx, cc); err != nil {
		return roles, types, conds, missing, err
	}
	for i, c := range defaultConditions {
		if ok, err := has("contract__condition_type", "code", c.code); err != nil || ok {
			if err != nil {
				return roles, types, conds, missing, err
			}
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO contract__condition_type (company_code, code, name, claim_class, default_kind, sort_order, is_active)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, cc, c.code, c.name, claimMain, c.kind, (i+1)*10, true); err != nil {
			return roles, types, conds, missing, err
		}
		if err := m.syncLegacyKind(ctx, cc, "code", c.code); err != nil {
			return roles, types, conds, missing, err
		}
		conds++
	}
	for _, d := range defaultDependents {
		if ok, err := has("contract__dependent_type", "code", d.code); err != nil || ok {
			if err != nil {
				return roles, types, conds, missing, err
			}
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO contract__dependent_type (company_code, code, name, method, effect, invoice_default, sort_order, is_active)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, cc, d.code, d.name, d.method, d.effect, d.invoiceDefault, d.sort, true); err != nil {
			return roles, types, conds, missing, err
		}
	}
	return roles, types, conds, missing, nil
}

// setupCompanyAction: Konsole (company) bzw. Aktion „Buchungskreis einrichten …“ (data.company_code).
func (m *Module) setupCompanyAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Company     string `json:"company"`
		CompanyCode string `json:"company_code"`
		Data        struct {
			CompanyCode string `json:"company_code"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.Company + in.CompanyCode + in.Data.CompanyCode)
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis (company) ist Pflicht")
	}
	if err := requireWrite(ctx, "ContractType", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	var roles, types, conds int
	var missing []string
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		var err error
		roles, types, conds, missing, err = m.setupCompany(ctx, cc)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	tags, err := m.setupTenantTags(ctx, cc)
	if err == nil {
		var supply int
		supply, err = m.setupSupplyTags(ctx, cc)
		tags += supply
	}
	switch {
	case errors.Is(err, sdk.ErrUnimplemented):
		tags = 0 // Tag-Plugin nicht gestartet
	case err != nil:
		return sdk.Response{}, err
	}
	rules, hint := m.setupDisplayRules(ctx)
	msg := fmt.Sprintf("Buchungskreis %s: %d Partnerrollen, %d Vertragsarten, %d Konditionsarten, %d Merkmale (Tags: Mieter, Versorgung), %d Darstellungsregeln angelegt",
		cc, roles, types, conds, tags, rules)
	if hint != "" {
		msg += " – " + hint
	}
	if len(missing) > 0 {
		msg += ". Ohne Rolle im Partnermodul nicht angelegt: " + strings.Join(missing, ", ") + " – Rolle anlegen und erneut einrichten"
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "partner_roles": roles, "contract_types": types, "condition_types": conds, "tags": tags, "display_rules": rules, "message": msg}}, nil
}
