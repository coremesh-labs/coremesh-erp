package opcost

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Ebenen der Abrechnung (seit 0.4.0):
//
//   - Abrechnungskonto (SettlementAccount) je Regelwerk: Text auf der
//     Abrechnung, Ausweisgruppe (z. B. Heiz- und Warmwasserkosten), Bindung
//     (je Vertrag zusammengefasst oder je Mietobjekt ausgewiesen), Eingabe am
//     Lauf (je Mieter und Wohnung, je Mieter, je Wohnung, gesamt) und
//     Kennzeichen (heizkostenrelevant, haushaltsnah § 35a). Die Codes sind die
//     Töpfe bzw. Ergebniskonten der Regeln.
//   - Eingaben (SettlementInput) am Lauf, solange er im Entwurf ist: Heizkosten
//     und CO2 je Mieter und Wohnung, WEG-Gesamtkosten, Vorauszahlungen …
//   - Ergebnis je Ebene (SettlementResult): Mieter, Mietobjekt, Konto, Ebene,
//     Betrag, Formel – aus dem Rechnen.
//   - Vorlagen (SettlementTemplate) je Regelwerk: Abrechnung an Mieter,
//     Gesamtschau eines Objekts, Excel-Export.

const (
	accountObject  = "SettlementAccount"
	inputObject    = "SettlementInput"
	resultObject   = "SettlementResult"
	templateObject = "SettlementTemplate"

	inputNone         = "NONE"
	inputTenantObject = "TENANT_OBJECT"
	inputTenant       = "TENANT"
	inputObjectOnly   = "OBJECT"
	inputTotal        = "TOTAL"
)

var (
	bindingOptions = []metamodel.Option{{Value: "CONTRACT", Label: "je Vertrag zusammengefasst", Short: "Vertrag"},
		{Value: "OBJECT", Label: "je Mietobjekt ausweisen", Short: "Mietobjekt"}}
	inputModeOptions = []metamodel.Option{{Value: inputNone, Label: "keine Eingabe", Short: "–"},
		{Value: inputTenantObject, Label: "je Mieter und Mietobjekt (z. B. Heizkosten, CO2)", Short: "Mieter/Objekt"},
		{Value: inputTenant, Label: "je Mieter (z. B. Vorauszahlungen)", Short: "Mieter"},
		{Value: inputObjectOnly, Label: "je Mietobjekt (z. B. WEG-Einzelabrechnung)", Short: "Objekt"},
		{Value: inputTotal, Label: "gesamt (z. B. WEG-Gesamtkosten)", Short: "gesamt"}}
	templateUsageOptions = []metamodel.Option{{Value: "TENANT_LETTER", Label: "Abrechnung an Mieter"},
		{Value: "OBJECT_OVERVIEW", Label: "Gesamtschau für ein Objekt", Short: "Gesamtschau"}, {Value: "EXCEL", Label: "Excel-Export"}}
	amountRe = regexp.MustCompile(`^-?\d+([.]\d+)?$`)
)

func (m *Module) settlementAccount() *crud.Entity {
	return &crud.Entity{
		Object: accountObject, Title: "Abrechnungskonten", Icon: "icon-list", Table: "opcost__account", Section: "Abrechnung",
		Keys: []string{"company_code", "definition", "code"}, Order: "company_code, definition, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code", "definition", "display_group", "input_mode"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "definition", Label: "Regelwerk", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupDefinition},
			{Key: "code", Label: "Konto (Topf bzw. Ergebniskonto der Regeln)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Text auf der Abrechnung", Short: "Text", Type: tText, Required: true, Listable: true},
			{Key: "display_group", Label: "Ausweisgruppe (z. B. Heiz- und Warmwasserkosten)", Type: tText, Listable: true},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum, Listable: true},
			{Key: "binding", Label: "Ausweis", Type: tSel, Listable: true, Options: bindingOptions},
			{Key: "input_mode", Label: "Eingabe am Lauf", Short: "Eingabe", Type: tSel, Listable: true, Options: inputModeOptions},
			{Key: "on_statement", Label: "Auf der Abrechnung ausweisen", Type: tBool, Group: "Kennzeichen"},
			{Key: "heating", Label: "Heizkostenrelevant (Aufstellung Messdienst)", Type: tBool, Group: "Kennzeichen"},
			{Key: "hhs", Label: "Haushaltsnahe Dienstleistung (§ 35a EStG)", Type: tBool, Group: "Kennzeichen"},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			rec["definition"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["definition"])))
			rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
			if !codeListRe.MatchString(crud.Str(rec["code"])) || strings.Contains(crud.Str(rec["code"]), ",") {
				return crud.Invalid("Konto: Buchstaben, Ziffern und _ (z. B. HEIZ)")
			}
			for k, def := range map[string]any{"sort_order": 0, "binding": "CONTRACT", "input_mode": inputNone, "on_statement": true, "heating": false, "hhs": false} {
				if rec[k] == nil || crud.Str(rec[k]) == "" {
					rec[k] = def
				}
			}
			rec["display_group"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["display_group"])))
			return nil
		},
	}
}

func (m *Module) settlementInput() *crud.Entity {
	return &crud.Entity{
		Object: inputObject, Title: "Eingaben (Abrechnung)", Icon: "icon-edit", Table: "opcost__input", Section: "Abrechnung",
		Keys: []string{"company_code", "definition", "period_from", "line_no"}, Order: "company_code, definition, period_from, account, contract_id, rent_object_id, line_no",
		Filters: []string{"company_code", "definition", "period_from", "account", "contract_id", "rent_object_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "definition", Label: "Regelwerk", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupDefinition},
			{Key: "period_from", Label: "Lauf (von)", Type: tDate, Required: true, Immutable: true},
			{Key: "line_no", Label: "Zeile", Type: tNum, ReadOnly: true},
			{Key: "account", Label: "Abrechnungskonto", Short: "Konto", Type: tText, Required: true, Listable: true,
				Lookup: &metamodel.Lookup{Object: accountObject, ValueField: "code", LabelFields: []string{"name"},
					Filters: map[string]string{"company_code": "company_code", "definition": "definition"}}},
			{Key: "contract_id", Label: "Vertrag (Mieter)", Type: tText, Listable: true},
			{Key: "rent_object_id", Label: "Mietobjekt", Type: tText, Listable: true},
			{Key: "amount", Label: "Betrag (EUR)", Type: tText, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText, Listable: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "remove", Label: "Löschen", Record: true,
			Confirm: "Eingabezeile löschen?"}, Handle: m.removeInputAction}},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			from, err := crud.ParseDate(rec["period_from"])
			if err != nil {
				return err
			}
			res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1 FROM opcost__input WHERE company_code = ? AND definition = ? AND period_from = ?`,
				rec["company_code"], strings.ToUpper(crud.Str(rec["definition"])), from)
			if err != nil {
				return err
			}
			rec["line_no"] = toInt(res.Rows[0][0])
			return nil
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			rec["definition"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["definition"])))
			from, err := crud.ParseDate(rec["period_from"])
			if err != nil {
				return err
			}
			rec["period_from"] = from
			k := runKey{CC: crud.Str(rec["company_code"]), Definition: crud.Str(rec["definition"]), From: from}
			status, _, err := m.runStatus(ctx, k)
			if err != nil {
				return err
			}
			if status != runDraft {
				return crud.Invalid("Lauf ist freigegeben – Eingaben nicht mehr änderbar")
			}
			acc := strings.ToUpper(strings.TrimSpace(crud.Str(rec["account"])))
			if m.text(ctx, `SELECT 1 FROM opcost__account WHERE company_code = ? AND definition = ? AND code = ?`, k.CC, k.Definition, acc) == "" {
				return crud.Invalid("Abrechnungskonto %q gibt es im Regelwerk %s nicht", acc, k.Definition)
			}
			rec["account"] = acc
			for _, f := range []string{"contract_id", "rent_object_id", "note"} {
				rec[f] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[f])))
			}
			cents, err := parseCents(crud.Str(rec["amount"]))
			if err != nil {
				return crud.Invalid("%v", err)
			}
			rec["amount"] = cents
			return nil
		},
		Decorate: func(_ context.Context, rec crud.Record) error {
			rec["amount"] = centsText(toInt(rec["amount"]))
			return nil
		},
	}
}

func (m *Module) settlementResult() *crud.Entity {
	return &crud.Entity{
		Object: resultObject, Title: "Ergebnis je Ebene", Icon: "icon-list", Table: "opcost__result", Section: "Abrechnung", ReadOnly: true,
		Keys: []string{"company_code", "definition", "period_from", "line_no"}, Order: "company_code, definition, period_from, contract_id, level, line_no",
		Filters: []string{"company_code", "definition", "period_from", "contract_id", "rent_object_id", "account", "level"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true},
			{Key: "definition", Label: "Regelwerk", Type: tText, Listable: true},
			{Key: "period_from", Label: "Lauf (von)", Type: tDate},
			{Key: "line_no", Label: "Zeile", Type: tNum},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Listable: true},
			{Key: "rent_object_id", Label: "Mietobjekt", Type: tText, Listable: true},
			{Key: "level", Label: "Ebene", Type: tNum, Listable: true},
			{Key: "account", Label: "Abrechnungskonto", Short: "Konto", Type: tText, Listable: true},
			{Key: "account_name", Label: "Text auf der Abrechnung", Short: "Text", Type: tText, Listable: true, Virtual: true},
			{Key: "amount", Label: "Betrag", Type: tText, Listable: true},
			{Key: "formula", Label: "Formel", Type: tText, Listable: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			rec["amount"] = centsText(toInt(rec["amount"]))
			rec["account_name"] = m.text(ctx, `SELECT name FROM opcost__account WHERE company_code = ? AND definition = ? AND code = ?`,
				rec["company_code"], rec["definition"], rec["account"])
			return nil
		},
	}
}

func (m *Module) settlementTemplate() *crud.Entity {
	return &crud.Entity{
		Object: templateObject, Title: "Vorlagen (Abrechnung)", Icon: "icon-file-text", Table: "opcost__template", Section: "Abrechnung",
		Keys: []string{"company_code", "definition", "code"}, Order: "company_code, definition, usage, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code", "definition", "usage"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "definition", Label: "Regelwerk", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupDefinition},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "usage", Label: "Verwendung", Type: tSel, Required: true, Listable: true, Options: templateUsageOptions},
			{Key: "template", Label: "Vorlage", Type: tArea},
			{Key: "note", Label: "Bemerkung", Type: tText, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["definition"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["definition"])))
			rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
			return nil
		},
	}
}

// removeInputAction: „Löschen“ – Eingabezeile eines Laufs im Entwurf.
func (m *Module) removeInputAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity(inputObject).ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	from, err := crud.ParseDate(key["period_from"])
	if err != nil {
		return sdk.Response{}, err
	}
	k := runKey{CC: crud.Str(key["company_code"]), Definition: crud.Str(key["definition"]), From: from}
	if err := requireAccess(ctx, inputObject, "update", k.CC); err != nil {
		return sdk.Response{}, err
	}
	status, _, err := m.runStatus(ctx, k)
	if err != nil {
		return sdk.Response{}, err
	}
	if status != runDraft {
		return sdk.Response{}, crud.Invalid("Lauf ist freigegeben – Eingaben nicht mehr änderbar")
	}
	if _, err := m.db.Exec(ctx, `DELETE FROM opcost__input WHERE company_code = ? AND definition = ? AND period_from = ? AND line_no = ?`,
		k.CC, k.Definition, k.From, toInt(key["line_no"])); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Eingabezeile gelöscht"}}, nil
}

// parseCents: "1.250,50" / "1250.5" / "" → Cent.
func parseCents(s string) (int64, error) {
	t := strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if t == "" {
		return 0, nil
	}
	if strings.Contains(t, ",") {
		t = strings.ReplaceAll(strings.ReplaceAll(t, ".", ""), ",", ".")
	}
	if !amountRe.MatchString(t) {
		return 0, fmt.Errorf("Betrag %q: Dezimalzahl erwartet, z. B. 1250,50", s)
	}
	neg := strings.HasPrefix(t, "-")
	whole, frac, _ := strings.Cut(strings.TrimPrefix(t, "-"), ".")
	if len(frac) > 2 {
		return 0, fmt.Errorf("Betrag %q: höchstens 2 Nachkommastellen", s)
	}
	frac += strings.Repeat("0", 2-len(frac))
	n, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok || !n.IsInt64() {
		return 0, fmt.Errorf("Betrag %q ist zu groß", s)
	}
	if neg {
		return -n.Int64(), nil
	}
	return n.Int64(), nil
}

// --- Eingabezeilen anlegen -----------------------------------------------------------------

type occupancyRow struct {
	Contract string `json:"contract_id"`
	Partner  string `json:"partner_id"`
	Object   string `json:"rent_object_id"`
	From     string `json:"from"`
	To       string `json:"to"`
}

// inputLinesAction: „Eingabezeilen anlegen“ – je Konto mit Eingabe am Lauf die
// fehlenden Zeilen aus der Belegung des Zeitraums (gespeichert beim Rechnen):
// je Mieter und Wohnung, je Mieter, je Wohnung oder eine Zeile gesamt.
// Vorhandene Zeilen bleiben unverändert; Zeilen mit Betrag 0, die nicht mehr
// zur Eingabeart der Konten passen, werden entfernt.
func (m *Module) inputLinesAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	k, err := m.runKeyOf(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireAccess(ctx, inputObject, "create", k.CC); err != nil {
		return sdk.Response{}, err
	}
	status, _, err := m.runStatus(ctx, k)
	if err != nil {
		return sdk.Response{}, err
	}
	if status != runDraft {
		return sdk.Response{}, crud.Invalid("Lauf ist freigegeben – keine neuen Eingaben")
	}
	accounts, err := m.query(ctx, `SELECT code, input_mode FROM opcost__account WHERE company_code = ? AND definition = ? AND is_active = ?
		AND input_mode <> ? ORDER BY sort_order, code`, []string{"code", "input_mode"}, k.CC, k.Definition, true, inputNone)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(accounts) == 0 {
		return sdk.Response{}, crud.Invalid("Regelwerk %s hat keine Abrechnungskonten mit Eingabe am Lauf", k.Definition)
	}
	var occ []occupancyRow
	if s := m.text(ctx, `SELECT occupancy FROM opcost__run WHERE company_code = ? AND definition = ? AND period_from = ?`, k.CC, k.Definition, k.From); s != "" {
		if err := json.Unmarshal([]byte(s), &occ); err != nil {
			return sdk.Response{}, err
		}
	}
	if len(occ) == 0 {
		return sdk.Response{}, crud.Invalid("Belegung unbekannt – Lauf zuerst rechnen (Rechnen liefert Mieter und Mietobjekte des Zeitraums)")
	}
	type line struct{ acc, contract, object string }
	seen := map[line]bool{}
	var want []line
	add := func(l line) {
		if !seen[l] {
			seen[l] = true
			want = append(want, l)
		}
	}
	sort.SliceStable(occ, func(i, j int) bool {
		return occ[i].Object < occ[j].Object || occ[i].Object == occ[j].Object && occ[i].From < occ[j].From
	})
	for _, a := range accounts {
		acc := crud.Str(a["code"])
		switch crud.Str(a["input_mode"]) {
		case inputTenantObject:
			for _, o := range occ {
				add(line{acc, o.Contract, o.Object})
			}
		case inputTenant:
			for _, o := range occ {
				add(line{acc, o.Contract, ""})
			}
		case inputObjectOnly:
			for _, o := range occ {
				add(line{acc, "", o.Object})
			}
		case inputTotal:
			add(line{acc, "", ""})
		}
	}
	var created, removed int
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		have := map[line]bool{}
		res, err := m.db.Query(ctx, `SELECT account, contract_id, rent_object_id, amount, line_no FROM opcost__input WHERE company_code = ? AND definition = ? AND period_from = ?`,
			k.CC, k.Definition, k.From)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			l := line{crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2])}
			if !seen[l] && toInt(r[3]) == 0 {
				if _, err := m.db.Exec(ctx, `DELETE FROM opcost__input WHERE company_code = ? AND definition = ? AND period_from = ? AND line_no = ?`,
					k.CC, k.Definition, k.From, toInt(r[4])); err != nil {
					return err
				}
				removed++
				continue
			}
			have[l] = true
		}
		next := toInt(m.text(ctx, `SELECT COALESCE(MAX(line_no), 0) FROM opcost__input WHERE company_code = ? AND definition = ? AND period_from = ?`,
			k.CC, k.Definition, k.From))
		for _, l := range want {
			if have[l] {
				continue
			}
			next++
			if _, err := m.db.Exec(ctx, `INSERT INTO opcost__input (company_code, definition, period_from, line_no, account, contract_id, rent_object_id, amount)
				VALUES (?, ?, ?, ?, ?, ?, ?, 0)`, k.CC, k.Definition, k.From, next, l.acc, nilIfEmpty(l.contract), nilIfEmpty(l.object)); err != nil {
				return err
			}
			created++
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("%d Eingabezeilen angelegt (Betrag 0 – bitte erfassen), %d leere Zeilen entfernt", created, removed)}}, nil
}
