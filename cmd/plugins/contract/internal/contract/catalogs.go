package contract

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Kataloge je Buchungskreis: Vertragsarten, Konditionsarten, Partnerrollen
// (Aktivierung aus dem Partnermodul) und Kontenfindung.

const (
	dirReceivable = "RECEIVABLE"
	dirPayable    = "PAYABLE"
	claimMain     = "MAIN"
	claimSecond   = "SECONDARY"
)

var (
	codeRe = regexp.MustCompile(`^[A-Z0-9_]{1,12}$`)

	directionOptions = []metamodel.Option{
		{Value: dirReceivable, Label: "Forderung (wir erhalten)"}, {Value: dirPayable, Label: "Verbindlichkeit (wir zahlen)"}}
	claimOptions = []metamodel.Option{
		{Value: claimMain, Label: "Hauptforderung"}, {Value: claimSecond, Label: "Nebenforderung (Kosten, Zinsen, Gebühren)"}}
	objectTypeOptions = []metamodel.Option{
		{Value: "RentObject", Label: "Mietobjekt"}, {Value: "Building", Label: "Gebäude"}, {Value: "BusinessEntity", Label: "Wirtschaftseinheit"}}
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
}

func (m *Module) contractTypeOf(ctx context.Context, cc, code string) (*typeRow, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, direction, main_role, range_key, object_types, needs_object, exclusive_objects,
		partner_account_required, parent_types, without_conditions FROM contract__contract_type WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
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
		ParentTypes: codeList(crud.Str(r[9])), WithoutConditions: crud.AsBool(r[10])}, nil
}

// --- Konditionsarten -----------------------------------------------------------------

func (m *Module) conditionType() *crud.Entity {
	return m.catalog("ConditionType", "Konditionsarten", "contract__condition_type", catalogFields(
		crud.Field{Key: "claim_class", Label: "Forderungsklasse", Type: tSel, Required: true, Listable: true, Options: claimOptions},
		crud.Field{Key: "is_advance", Label: "Vorauszahlung (wird abgerechnet)", Type: tBool, Listable: true},
		crud.Field{Key: "clearing_order", Label: "Verrechnungsreihenfolge (klein = zuerst)", Type: tNum, Listable: true},
		crud.Field{Key: "tax_code", Label: "Steuerkennzeichen", Type: tText},
		crud.Field{Key: "installments", Label: "Monatsraten bei einmaligen Konditionen (z. B. Kaution: 3)", Type: tNum},
	), func(_ context.Context, rec crud.Record) error {
		defaults(rec, map[string]any{"is_advance": false, "sort_order": 0, "installments": 1})
		if n := toInt(rec["installments"]); n < 1 || n > 60 {
			return crud.Invalid("Monatsraten 1 bis 60")
		}
		if rec["clearing_order"] == nil || crud.Str(rec["clearing_order"]) == "" {
			rec["clearing_order"] = map[string]int{claimMain: 30, claimSecond: 10}[crud.Str(rec["claim_class"])]
		}
		rec["tax_code"] = nilIfEmpty(trimUpper(rec["tax_code"]))
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
		Filters: []string{"company_code", "contract_type", "condition_type"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_type", Label: "Vertragsart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ContractType")},
			{Key: "condition_type", Label: "Konditionsart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ConditionType")},
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
	nr, name, err := m.glAccount(ctx, cc, crud.Str(rec["account_number"]))
	if err != nil {
		return err
	}
	if old == nil {
		rec["account_number"] = nr // wie im Hauptbuch geführt (Schlüssel)
	}
	rec["account_name"] = nilIfEmpty(name)
	// Höchstens ein Standardkonto je Vertragsart und Konditionsart.
	if crud.AsBool(rec["is_default"]) {
		_, err := m.db.Exec(ctx, `UPDATE contract__account SET is_default = ? WHERE company_code = ? AND contract_type = ? AND condition_type = ? AND account_number <> ?`,
			false, cc, rec["contract_type"], rec["condition_type"], rec["account_number"])
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
	return "", "", crud.Invalid("Sachkonto %s gibt es im Buchungskreis %s nicht", number, cc)
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
	{"KT", "Mietkaution", dirReceivable, "TENANT", "RentObject", false, false, "MV,GM,SP", ""},
	{"DA", "Darlehen (aufgenommen)", dirPayable, "CREDITOR", "", false, false, "", "KR"}, // Auszahlung ist keine Gutschrift
	{"DV", "Darlehen (vergeben)", dirReceivable, "DEBITOR", "", false, false, "", "DR"},
	{"WH", "Hausgeld an WEG (als Eigentümer)", dirPayable, "CREDITOR", "RentObject", true, false, "", ""},
	{"GS", "Grundsteuer", dirPayable, "AUTHORITY", "", false, false, "", ""}, // an die Behörde (Rolle Behörde / Amt)
	{"BK", "Bankkonto", dirPayable, "CREDITOR", "", false, false, "", ""},
	{"KK", "Kreditkarte", dirPayable, "CREDITOR", "", false, false, "BK", ""},
}

// withoutConditionsDefault: Vertragsarten, die ohne Konditionen aktiviert werden
// dürfen (Vorschlag; an der Vertragsart einstellbar).
var withoutConditionsDefault = map[string]bool{"WH": true}

var defaultConditions = []struct {
	code, name, claim   string
	advance             bool
	order, installments int
}{
	{"KM", "Kaltmiete", claimMain, false, 30, 1},
	{"NK", "Betriebskosten-Vorauszahlung", claimMain, true, 30, 1},
	{"HK", "Heizkosten-Vorauszahlung", claimMain, true, 30, 1},
	{"ST", "Stellplatzmiete", claimMain, false, 30, 1},
	{"HG", "Hausgeld", claimMain, true, 30, 1},
	{"EN", "Entgelt / Prämie", claimMain, false, 30, 1},
	{"MM", "Mietminderung", claimMain, false, 30, 1},
	{"BG", "Bereitstellungsgebühr", claimMain, false, 30, 1},
	{"MG", "Mahngebühr", claimSecond, false, 10, 1},
	{"ZI", "Verzugszinsen", claimSecond, false, 20, 1},
	{"KA", "Mietkaution", claimMain, false, 30, 3},
	{"DZ", "Darlehenszinsen", claimMain, false, 30, 1},
	{"DT", "Tilgung", claimMain, false, 30, 1},
	{"DS", "Sondertilgung", claimMain, false, 30, 1},
	{"AZ", "Darlehensauszahlung", claimMain, false, 30, 1},
	{"HV", "Hausgeld-Vorauszahlung (an WEG)", claimMain, true, 30, 1},
	{"RZ", "Vorauszahlung Erhaltungsrücklage (an WEG)", claimMain, true, 30, 1},
	{"VZ", "Vorauszahlung Betriebskosten (Versorger)", claimMain, true, 30, 1},
	{"GV", "Grundsteuer-Vorauszahlung", claimMain, true, 30, 1},
	{"KF", "Kontoführungsentgelt", claimMain, false, 30, 1},
	{"KJ", "Kartengebühr", claimMain, false, 30, 1},
}

// setupCompany legt fehlende Vorschlagswerte an: Partnerrollen (soweit im
// Partnermodul vorhanden), Vertragsarten (deren Rolle aktiviert ist) und
// Konditionsarten.
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
			object_types, exclusive_objects, parent_types, credit_document_type, sort_order, is_active, without_conditions) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cc, t.code, t.name, t.direction, t.role, t.code, t.needsObject, nilIfEmpty(t.objects), t.exclusive, nilIfEmpty(t.parents),
			nilIfEmpty(t.credit), (i+1)*10, true, withoutConditionsDefault[t.code]); err != nil {
			return roles, types, conds, missing, err
		}
		types++
	}
	for i, c := range defaultConditions {
		if ok, err := has("contract__condition_type", "code", c.code); err != nil || ok {
			if err != nil {
				return roles, types, conds, missing, err
			}
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO contract__condition_type (company_code, code, name, claim_class, is_advance, clearing_order, installments,
			sort_order, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, c.code, c.name, c.claim, c.advance, c.order, c.installments, (i+1)*10, true); err != nil {
			return roles, types, conds, missing, err
		}
		conds++
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
	switch {
	case errors.Is(err, sdk.ErrUnimplemented):
		tags = 0 // Tag-Plugin nicht gestartet
	case err != nil:
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Buchungskreis %s: %d Partnerrollen, %d Vertragsarten, %d Konditionsarten, %d Mieter-Merkmale (Tags) angelegt", cc, roles, types, conds, tags)
	if len(missing) > 0 {
		msg += ". Ohne Rolle im Partnermodul nicht angelegt: " + strings.Join(missing, ", ") + " – Rolle anlegen und erneut einrichten"
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "partner_roles": roles, "contract_types": types, "condition_types": conds, "tags": tags, "message": msg}}, nil
}
