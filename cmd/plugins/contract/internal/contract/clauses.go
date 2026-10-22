package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
)

// Vertragsklauseln (seit 0.39.0): Klauselsets je Buchungskreis für bestimmte
// Vertragsarten, Klauseln mit Paragraph, Absatz (0 = allgemeiner Text des
// Paragraphen), Text in Markdown, einer Bedingung, Bewertung und Hinweis.
//
// Am Vertrag: „Vertragsklauseln erstellen …“ wertet die Bedingungen aus und kopiert
// die zutreffenden Klauseln mit der gedruckten Nummer (lückenlos) in den Vertrag;
// Verweise {ref:SCHLÜSSEL} werden dabei zu „§ n“ bzw. „§ n Abs. m“. Nur „erneuern“
// ändert die Kopie; ein Hinweis am Vertrag zeigt, wenn sich das Set seither geändert
// hat (Prüfsumme). „Vertragsdokument (PDF) …“ setzt die übrigen Platzhalter mit den
// aktuellen Vertragsdaten ein (clausedoc.go). „Vertragsklauseln löschen …“, wenn der
// unterschriebene Vertrag im DMS am Vertrag hängt oder der Vertrag nicht zustande kam.

const (
	clauseSetObject  = "ContractClauseSet"
	clauseObject     = "ContractClause"
	clauseCopyObject = "ContractClauseCopy"
)

// Quellen der Bedingung einer Klausel.
const (
	condContractTag = "CONTRACT_TAG"
	condObjectTag   = "OBJECT_TAG"
	condPartnerTag  = "PARTNER_TAG"
	condField       = "CONTRACT_FIELD"
	condCondition   = "CONDITION"
	condText        = "TEXT"
)

// Gründe für das Löschen der Kopie.
const (
	clauseDelSigned       = "SIGNED"
	clauseDelNotConcluded = "NOT_CONCLUDED"
)

var (
	clauseKeyRe = regexp.MustCompile(`^[A-Z0-9_]{1,30}$`)
	refRe       = regexp.MustCompile(`\{ref:([A-Za-z0-9_]+)\}`)
	textOnlyRe  = regexp.MustCompile(`^\s*\{text:([A-Za-z0-9_]+)\}\s*$`)

	condSourceOptions = []metamodel.Option{
		{Value: condContractTag, Label: "Tag am Vertrag", Short: "Tag Vertrag"},
		{Value: condObjectTag, Label: "Tag am Mietobjekt (Hauptobjekt)", Short: "Tag Objekt"},
		{Value: condPartnerTag, Label: "Tag an einem Vertragspartner", Short: "Tag Partner"},
		{Value: condField, Label: "Feld des Vertrags (z. B. valid_to)", Short: "Feld"},
		{Value: condCondition, Label: "Kondition vorhanden (Konditionsart)", Short: "Kondition"},
		{Value: condText, Label: "Vertragstext vorhanden (Textkategorie)", Short: "Text"},
	}
	ratingOptions = []metamodel.Option{
		{Value: "OK", Label: "geprüft", Short: "✓"},
		{Value: "CHECK", Label: "prüfen", Short: "!"},
		{Value: "DISPUTED", Label: "umstritten", Short: "?"},
	}
	clauseDeleteOptions = []metamodel.Option{
		{Value: clauseDelSigned, Label: "Unterschriebener Vertrag liegt im DMS am Vertrag"},
		{Value: clauseDelNotConcluded, Label: "Vertrag ist nicht zustande gekommen"},
	}
)

// --- Klauselsets und Klauseln ----------------------------------------------------------

func (m *Module) clauseSet() *crud.Entity {
	e := m.catalog(clauseSetObject, "Klauselsets", "contract__clause_set", catalogFields(
		crud.Field{Key: "contract_types", Label: "Vertragsarten (Komma-Liste, z. B. MV,GM; leer = alle)", Short: "Vertragsarten", Type: tText, Listable: true},
		crud.Field{Key: "title", Label: "Titel des Dokuments (z. B. Mietvertrag)", Short: "Titel", Type: tText, Listable: true},
		crud.Field{Key: "template", Label: "Vorlage für das PDF (Verwendung CONTRACT_DOCUMENT; Vorschlag beim Drucken)", Short: "Vorlage", Type: tText,
			Lookup: &metamodel.Lookup{Object: "TemplateDefinition", ValueField: "code", LabelFields: []string{"name"}}},
		crud.Field{Key: "own_label", Label: "Bezeichnung unserer Partei (z. B. Vermieter)", Short: "Unsere Partei", Type: tText},
		crud.Field{Key: "own_roles", Label: "Unsere Partei: Rollen am Objekt in dieser Reihenfolge (z. B. OWNER,MANAGER; leer = Partner des Buchungskreises)", Type: tText},
		crud.Field{Key: "partner_label", Label: "Bezeichnung der Vertragspartner (z. B. Mieter)", Short: "Partner", Type: tText},
	), func(_ context.Context, rec crud.Record) error {
		var types []string
		for _, t := range strings.Split(crud.Str(rec["contract_types"]), ",") {
			if t = strings.ToUpper(strings.TrimSpace(t)); t != "" {
				types = append(types, t)
			}
		}
		rec["contract_types"] = nilIfEmpty(strings.Join(types, ","))
		code, _, _ := strings.Cut(trimUpper(rec["template"]), "|")
		rec["template"] = nilIfEmpty(code)
		return nil
	})
	e.Icon = "icon-list"
	e.Sections = []metamodel.SectionDefinition{
		{Key: "klauseln", Title: "Klauseln", Relation: &metamodel.Relation{Object: clauseObject, ForeignKey: "set_code",
			Match:   map[string]string{"company_code": "company_code", "set_code": "code"},
			Columns: []string{"paragraph", "subsection", "clause_key", "title", "condition", "rating", "is_active"}}},
	}
	return e
}

func (m *Module) clause() *crud.Entity {
	return &crud.Entity{
		Object: clauseObject, Customizing: true, Title: "Klauseln", Icon: "icon-list", Table: "contract__clause", Section: "Einstellungen",
		Keys: []string{"company_code", "set_code", "clause_key"}, Order: "company_code, set_code, paragraph, subsection, clause_key",
		StatusField: "is_active", TitleField: "title",
		Filters: []string{"company_code", "set_code", "rating", "cond_source"}, Search: []string{"clause_key", "title", "text", "note"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "set_code", Label: "Klauselset", Short: "Set", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup(clauseSetObject)},
			{Key: "clause_key", Label: "Schlüssel (für Verweise {ref:…}; A–Z, 0–9, _)", Short: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "paragraph", Label: "Paragraph (Verschieben = Nummer ändern)", Short: "§", Type: tNum, Required: true, Listable: true},
			{Key: "subsection", Label: "Absatz (0 = allgemeiner Text des Paragraphen; gleiche Nummer = derselbe Absatz)", Short: "Abs.", Type: tNum, Listable: true},
			{Key: "title", Label: "Titel (bei Absatz 0 die Überschrift des Paragraphen)", Short: "Titel", Type: tText, Listable: true, Wide: true},
			{Key: "text", Label: "Text (Markdown: **fett**, *kursiv*, Listen mit - / 1. / a); Platzhalter {quelle:feld})", Type: tArea, Wide: true},
			{Key: "cond_source", Label: "Bedingung: Quelle (leer = immer)", Short: "Quelle", Type: tSel, Options: condSourceOptions, Group: "Bedingung", Trigger: true},
			{Key: "cond_name", Label: "Bedingung: Tag, Feld, Konditionsart bzw. Textkategorie", Short: "Name", Type: tText, Group: "Bedingung"},
			{Key: "cond_values", Label: "Bedingung: Werte (Komma-Liste; leer = vorhanden)", Short: "Werte", Type: tText, Group: "Bedingung"},
			{Key: "cond_negate", Label: "Bedingung muss NICHT erfüllt sein", Short: "nicht", Type: tBool, Group: "Bedingung"},
			{Key: "condition", Label: "Bedingung", Type: tText, Listable: true, Virtual: true},
			{Key: "rating", Label: "Bewertung", Type: tSel, Listable: true, Options: ratingOptions, Group: "Bewertung"},
			{Key: "note", Label: "Hinweis / Bearbeitungshinweis (wird nicht gedruckt)", Short: "Hinweis", Type: tArea, Group: "Bewertung"},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, clauseObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["clause_key"] = trimUpper(rec["clause_key"])
				if !clauseKeyRe.MatchString(crud.Str(rec["clause_key"])) {
					return crud.Invalid("Schlüssel %q: A–Z, 0–9, _ (höchstens 30 Zeichen)", crud.Str(rec["clause_key"]))
				}
			}
			if toInt(rec["paragraph"]) < 1 {
				return crud.Invalid("Paragraph: ganze Zahl ab 1")
			}
			if toInt(rec["subsection"]) < 0 {
				return crud.Invalid("Absatz: ganze Zahl ab 0")
			}
			if crud.Str(rec["cond_source"]) == "" {
				rec["cond_name"], rec["cond_values"], rec["cond_negate"] = nil, nil, false
			} else if strings.TrimSpace(crud.Str(rec["cond_name"])) == "" {
				return crud.Invalid("Bedingung: Name fehlt (Tag, Feld, Konditionsart bzw. Textkategorie)")
			}
			rec["changed_at"], rec["changed_by"] = now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
			return nil
		},
		Decorate: func(_ context.Context, rec crud.Record) error {
			rec["condition"] = conditionText(rec)
			return nil
		},
	}
}

// conditionText: Bedingung zum Anzeigen, z. B. „Tag am Vertrag KAUTION = BUERGSCHAFT“.
func conditionText(rec crud.Record) string {
	src := crud.Str(rec["cond_source"])
	if src == "" {
		return "immer"
	}
	label := src
	for _, o := range condSourceOptions {
		if o.Value == src {
			label = o.Short
		}
	}
	s := label + " " + crud.Str(rec["cond_name"])
	if v := crud.Str(rec["cond_values"]); v != "" {
		s += " = " + v
	} else {
		s += " vorhanden"
	}
	if crud.AsBool(rec["cond_negate"]) {
		s = "nicht: " + s
	}
	return s
}

// --- Kopie am Vertrag ------------------------------------------------------------------

func (m *Module) clauseCopy() *crud.Entity {
	return &crud.Entity{
		Object: clauseCopyObject, Title: "Vertragsklauseln", Icon: "icon-list", Table: "contract__clause_copy", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "seq"}, Order: "company_code, contract_id, seq", ReadOnly: true,
		Filters: []string{"company_code", "contract_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, Lookup: lookupCC},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Listable: true, Lookup: contractLookup},
			{Key: "seq", Label: "Lfd. Nr.", Type: tNum},
			{Key: "number", Label: "Nummer", Short: "Nr.", Type: tText, Listable: true, Virtual: true},
			{Key: "paragraph_no", Label: "Paragraph", Type: tNum},
			{Key: "subsection_no", Label: "Absatz", Type: tNum},
			{Key: "title", Label: "Titel", Type: tText, Listable: true},
			{Key: "text", Label: "Text", Type: tArea, Listable: true},
			{Key: "clause_key", Label: "Klausel", Type: tText, Listable: true},
			{Key: "rating", Label: "Bewertung", Type: tSel, Listable: true, Options: ratingOptions},
		},
		Access: &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Decorate: func(_ context.Context, rec crud.Record) error {
			rec["number"] = clauseNumber(toInt(rec["paragraph_no"]), toInt(rec["subsection_no"]))
			return nil
		},
	}
}

var clauseCopySection = metamodel.SectionDefinition{Key: "vertragsklauseln", Title: "Vertragsklauseln", Collapsed: true,
	Relation: &metamodel.Relation{Object: clauseCopyObject, ForeignKey: "contract_id", Match: match(),
		Columns: []string{"number", "title", "text", "clause_key", "rating"}}}

// clauseNumber: „§ 5“ bzw. „§ 5 Abs. 2“.
func clauseNumber(p, s int64) string {
	if s == 0 {
		return fmt.Sprintf("§ %d", p)
	}
	return fmt.Sprintf("§ %d Abs. %d", p, s)
}

// clauseRow: Klausel eines Sets.
type clauseRow struct {
	Key, Title, Text, Source, Name, Values, Rating string
	Paragraph, Subsection                          int64
	Negate                                         bool
}

// clauseSetRow: Kopf eines Klauselsets.
type clauseSetRow struct {
	Code, Name, Types, Title, Template, OwnLabel, OwnRoles, PartnerLabel string
	Active                                                               bool
}

func (m *Module) clauseSetOf(ctx context.Context, cc, code string) (*clauseSetRow, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, contract_types, title, template, own_label, own_roles, partner_label, is_active
		FROM contract__clause_set WHERE company_code = ? AND code = ?`, cc, code)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Klauselset %s gibt es im Buchungskreis %s nicht", code, cc)
	}
	r := res.Rows[0]
	return &clauseSetRow{Code: crud.Str(r[0]), Name: crud.Str(r[1]), Types: crud.Str(r[2]), Title: crud.Str(r[3]), Template: crud.Str(r[4]),
		OwnLabel: crud.Str(r[5]), OwnRoles: crud.Str(r[6]), PartnerLabel: crud.Str(r[7]), Active: crud.AsBool(r[8])}, nil
}

// fits: Das Set gilt für die Vertragsart (leer = alle).
func (s *clauseSetRow) fits(contractType string) bool {
	if strings.TrimSpace(s.Types) == "" {
		return true
	}
	return slices.Contains(strings.Split(s.Types, ","), contractType)
}

// clausesOf: aktive Klauseln eines Sets in der Reihenfolge Paragraph, Absatz, Schlüssel.
func (m *Module) clausesOf(ctx context.Context, cc, set string) ([]clauseRow, error) {
	res, err := m.db.Query(ctx, `SELECT clause_key, title, text, cond_source, cond_name, cond_values, rating, paragraph, subsection, cond_negate
		FROM contract__clause WHERE company_code = ? AND set_code = ? AND is_active = ? ORDER BY paragraph, subsection, clause_key`, cc, set, true)
	if err != nil {
		return nil, err
	}
	out := make([]clauseRow, len(res.Rows))
	for i, r := range res.Rows {
		out[i] = clauseRow{Key: crud.Str(r[0]), Title: crud.Str(r[1]), Text: crud.Str(r[2]), Source: crud.Str(r[3]), Name: strings.TrimSpace(crud.Str(r[4])),
			Values: crud.Str(r[5]), Rating: crud.Str(r[6]), Paragraph: toInt(r[7]), Subsection: toInt(r[8]), Negate: crud.AsBool(r[9])}
	}
	return out, nil
}

// clausesHash: Prüfsumme der aktiven Klauseln eines Sets (Hinweis „geändert seit der Kopie“).
func clausesHash(cs []clauseRow) string {
	h := sha256.New()
	for _, c := range cs {
		fmt.Fprintf(h, "%s\x1f%d\x1f%d\x1f%s\x1f%s\x1f%s\x1f%s\x1f%s\x1f%t\x1e", c.Key, c.Paragraph, c.Subsection, c.Title, c.Text, c.Source, c.Name, c.Values, c.Negate)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// clausesEnabled: Vertragsart mit Vertragsklauseln und Vertragsdokument.
func (m *Module) clausesEnabled(ctx context.Context, cc, contractType string) bool {
	res, err := m.db.Query(ctx, `SELECT clauses_enabled FROM contract__contract_type WHERE company_code = ? AND code = ?`, cc, contractType)
	return err == nil && len(res.Rows) > 0 && crud.AsBool(res.Rows[0][0])
}

// decorateClauses: Klausel-Aktionen nach Vertragsart und Kopie ausblenden (Erstellen und
// Erneuern nur mit „Vertragsklauseln“ an der Vertragsart; PDF und Löschen nur mit Kopie);
// Hinweis, wenn sich das Klauselset seit der Kopie geändert hat.
func (m *Module) decorateClauses(ctx context.Context, rec crud.Record) error {
	set := crud.Str(rec["clause_set"])
	cc := crud.Str(rec["company_code"])
	hidden, _ := rec["_hidden_actions"].([]any)
	switch enabled := m.clausesEnabled(ctx, cc, crud.Str(rec["contract_type"])); {
	case set == "" && enabled:
		hidden = append(hidden, "renewClauses", "clauseDocument", "deleteClauses")
	case set == "":
		hidden = append(hidden, "createClauses", "renewClauses", "clauseDocument", "deleteClauses")
	case enabled:
		hidden = append(hidden, "createClauses")
	default:
		hidden = append(hidden, "createClauses", "renewClauses")
	}
	rec["_hidden_actions"] = hidden
	if set == "" {
		return nil
	}
	cs, err := m.clausesOf(ctx, cc, set)
	if err != nil {
		return nil
	}
	res, err := m.db.Query(ctx, `SELECT clauses_hash FROM contract__contract WHERE company_code = ? AND contract_id = ?`, cc, rec["contract_id"])
	if err != nil || len(res.Rows) == 0 {
		return nil
	}
	if clausesHash(cs) != crud.Str(res.Rows[0][0]) {
		rec["clause_state"] = "Klauseln des Sets " + set + " haben sich seit der Kopie geändert – „Vertragsklauseln erneuern“"
	} else {
		rec["clause_state"] = "Kopie entspricht dem Klauselset " + set
	}
	return nil
}

// copiedClause: Klausel in der Kopie.
type copiedClause struct {
	clauseRow
	P, S int64 // gedruckte Nummer
}

// copyClauses: Bedingungen auswerten, gedruckte Nummern vergeben, Verweise auflösen.
// Liefert die Kopie und die entfallenen Klauseln mit Grund.
func (m *Module) copyClauses(ctx context.Context, d *clauseData, cs []clauseRow) ([]copiedClause, []string, error) {
	var in []clauseRow
	var dropped []string
	for _, c := range cs {
		ok, why, err := d.holds(ctx, c)
		if err != nil {
			return nil, nil, fmt.Errorf("Klausel %s: %w", c.Key, err)
		}
		if ok {
			if t := textOnlyRe.FindStringSubmatch(c.Text); t != nil && strings.TrimSpace(d.text(ctx, t[1])) == "" {
				ok, why = false, "Vertragstext "+t[1]+" leer"
			}
		}
		if !ok {
			dropped = append(dropped, c.Key+" ("+why+")")
			continue
		}
		in = append(in, c)
	}
	var out []copiedClause
	var p, s, lastPara, lastSub int64 = 0, 0, -1, -1
	for _, c := range in {
		if c.Paragraph != lastPara {
			p, s, lastPara, lastSub = p+1, 0, c.Paragraph, -1
		}
		if c.Subsection != lastSub {
			if c.Subsection > 0 {
				s++
			}
			lastSub = c.Subsection
		}
		sub := s
		if c.Subsection == 0 {
			sub = 0
		}
		out = append(out, copiedClause{clauseRow: c, P: p, S: sub})
	}
	num := map[string]string{}
	for _, c := range out {
		num[c.Key] = clauseNumber(c.P, c.S)
	}
	var missing []string
	for i := range out {
		out[i].Text = refRe.ReplaceAllStringFunc(out[i].Text, func(t string) string {
			k := strings.ToUpper(refRe.FindStringSubmatch(t)[1])
			if n, ok := num[k]; ok {
				return n
			}
			missing = append(missing, out[i].Key+" → "+k)
			return "§ ?"
		})
	}
	if len(missing) > 0 {
		dropped = append(dropped, "Verweis auf entfallene oder fehlende Klausel: "+strings.Join(missing, ", "))
	}
	return out, dropped, nil
}

// --- Aktionen am Vertrag ---------------------------------------------------------------

type clauseActionInput struct {
	ID   string `json:"id"`
	Data struct {
		Set      string `json:"cl_set"`
		Template string `json:"cl_template"`
		Reason   string `json:"cl_reason"`
	} `json:"data"`
}

func (m *Module) clauseInput(ctx context.Context, req sdk.Request) (*clauseActionInput, *contractRow, error) {
	var in clauseActionInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return nil, nil, err
	}
	c, err := m.contractOfID(ctx, in.ID)
	if err != nil {
		return nil, nil, err
	}
	if err := requireWrite(ctx, "Contract", "update", c.CompanyCode); err != nil {
		return nil, nil, err
	}
	return &in, c, nil
}

// copyState: Set und Stand der Kopie am Vertrag.
func (m *Module) copyState(ctx context.Context, c *contractRow) (set, at string) {
	res, err := m.db.Query(ctx, `SELECT clause_set, clauses_at FROM contract__contract WHERE company_code = ? AND contract_id = ?`, c.CompanyCode, c.ID)
	if err != nil || len(res.Rows) == 0 {
		return "", ""
	}
	return crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1])
}

func (m *Module) createClausesAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	in, c, err := m.clauseInput(ctx, req)
	if err != nil {
		return sdk.Response{}, err
	}
	if set, _ := m.copyState(ctx, c); set != "" {
		return sdk.Response{}, crud.Invalid("Vertrag %s hat schon Vertragsklauseln aus dem Set %s – „Vertragsklauseln erneuern“ oder zuerst löschen", c.ID, set)
	}
	return m.writeClauses(ctx, c, strings.TrimSpace(in.Data.Set), "erstellt")
}

func (m *Module) renewClausesAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	_, c, err := m.clauseInput(ctx, req)
	if err != nil {
		return sdk.Response{}, err
	}
	set, _ := m.copyState(ctx, c)
	if set == "" {
		return sdk.Response{}, crud.Invalid("Vertrag %s hat keine Vertragsklauseln – „Vertragsklauseln erstellen …“", c.ID)
	}
	return m.writeClauses(ctx, c, set, "erneuert")
}

// writeClauses: Kopie (neu) schreiben und den Stand am Vertrag festhalten.
func (m *Module) writeClauses(ctx context.Context, c *contractRow, setCode, verb string) (sdk.Response, error) {
	if setCode == "" {
		return sdk.Response{}, crud.Invalid("Klauselset fehlt")
	}
	if !m.clausesEnabled(ctx, c.CompanyCode, c.Type) {
		return sdk.Response{}, crud.Invalid("Vertragsart %s hat keine Vertragsklauseln (Einstellungen → Vertragsarten → „Vertragsklauseln und Vertragsdokument“)", c.Type)
	}
	set, err := m.clauseSetOf(ctx, c.CompanyCode, setCode)
	if err != nil {
		return sdk.Response{}, err
	}
	if !set.Active || !set.fits(c.Type) {
		return sdk.Response{}, crud.Invalid("Klauselset %s ist inaktiv oder gilt nicht für die Vertragsart %s", set.Code, c.Type)
	}
	cs, err := m.clausesOf(ctx, c.CompanyCode, set.Code)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(cs) == 0 {
		return sdk.Response{}, crud.Invalid("Klauselset %s hat keine aktiven Klauseln", set.Code)
	}
	d, err := m.newClauseData(ctx, c, set)
	if err != nil {
		return sdk.Response{}, err
	}
	out, dropped, err := m.copyClauses(ctx, d, cs)
	if err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.db.Exec(ctx, `DELETE FROM contract__clause_copy WHERE company_code = ? AND contract_id = ?`, c.CompanyCode, c.ID); err != nil {
		return sdk.Response{}, err
	}
	for i, cl := range out {
		if _, err := m.db.Exec(ctx, `INSERT INTO contract__clause_copy (company_code, contract_id, seq, clause_key, paragraph_no, subsection_no, title, text, rating)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.CompanyCode, c.ID, i+1, cl.Key, cl.P, cl.S, nilIfEmpty(cl.Title), nilIfEmpty(cl.Text), nilIfEmpty(cl.Rating)); err != nil {
			return sdk.Response{}, err
		}
	}
	if _, err := m.db.Exec(ctx, `UPDATE contract__contract SET clause_set = ?, clauses_at = ?, clauses_by = ?, clauses_hash = ? WHERE company_code = ? AND contract_id = ?`,
		set.Code, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), clausesHash(cs), c.CompanyCode, c.ID); err != nil {
		return sdk.Response{}, err
	}
	paras := 0
	if len(out) > 0 {
		paras = int(out[len(out)-1].P)
	}
	msg := fmt.Sprintf("Vertrag %s: Vertragsklauseln %s aus %s – %d Klauseln in %d Paragraphen", c.ID, verb, set.Code, len(out), paras)
	var disputed []string
	for _, cl := range out {
		if cl.Rating == "DISPUTED" || cl.Rating == "CHECK" {
			disputed = append(disputed, cl.Key+" ("+clauseNumber(cl.P, cl.S)+")")
		}
	}
	if len(disputed) > 0 {
		msg += "; zu prüfen bzw. umstritten: " + strings.Join(disputed, ", ")
	}
	if len(dropped) > 0 {
		msg += "; entfallen: " + strings.Join(dropped, "; ")
	}
	return sdk.Response{Payload: map[string]any{"id": c.CompanyCode + "|" + c.ID, "message": msg}}, nil
}

func (m *Module) deleteClausesAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	in, c, err := m.clauseInput(ctx, req)
	if err != nil {
		return sdk.Response{}, err
	}
	if set, _ := m.copyState(ctx, c); set == "" {
		return sdk.Response{}, crud.Invalid("Vertrag %s hat keine Vertragsklauseln", c.ID)
	}
	switch in.Data.Reason {
	case clauseDelSigned:
		if !m.hasFiledDocument(ctx, c) {
			return sdk.Response{}, crud.Invalid("Am Vertrag %s hängt kein abgelegtes Dokument – zuerst den unterschriebenen Vertrag im DMS ablegen und mit dem Vertrag verknüpfen", c.ID)
		}
	case clauseDelNotConcluded:
		if c.Status != statusDraft {
			return sdk.Response{}, crud.Invalid("Vertrag %s ist kein Entwurf (Status %s) – „nicht zustande gekommen“ gilt nur für Entwürfe", c.ID, c.Status)
		}
	default:
		return sdk.Response{}, crud.Invalid("Grund fehlt")
	}
	if _, err := m.db.Exec(ctx, `DELETE FROM contract__clause_copy WHERE company_code = ? AND contract_id = ?`, c.CompanyCode, c.ID); err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.db.Exec(ctx, `UPDATE contract__contract SET clause_set = NULL, clauses_at = NULL, clauses_by = NULL, clauses_hash = NULL
		WHERE company_code = ? AND contract_id = ?`, c.CompanyCode, c.ID); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": c.CompanyCode + "|" + c.ID, "message": "Vertrag " + c.ID + ": Vertragsklauseln gelöscht"}}, nil
}

// hasFiledDocument: Ein abgelegtes DMS-Dokument ist mit dem Vertrag verknüpft.
func (m *Module) hasFiledDocument(ctx context.Context, c *contractRow) bool {
	resp, err := m.services.Call(ctx, "DmsDocument", "list", map[string]any{"query": map[string]any{
		"company_code": c.CompanyCode, "ref_object": "Contract", "ref_key": c.CompanyCode + "|" + c.ID, "status": "FILED"}, "limit": 1})
	if err != nil {
		return false
	}
	var l struct {
		Items []any `json:"items"`
	}
	return sdk.Decode(resp.Payload, &l) == nil && len(l.Items) > 0
}

// clauseFormState: Auswahl des Klauselsets bzw. der Vorlage.
func (m *Module) clauseFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	key, err := m.set.Entity("Contract").ParseID(req.ID)
	if err != nil {
		return st, nil
	}
	c, err := m.contractOf(ctx, crud.Str(key["company_code"]), crud.Str(key["contract_id"]))
	if err != nil {
		return st, nil
	}
	switch req.Action {
	case "createClauses":
		res, err := m.db.Query(ctx, `SELECT code, name, contract_types FROM contract__clause_set WHERE company_code = ? AND is_active = ? ORDER BY sort_order, code`,
			c.CompanyCode, true)
		if err != nil {
			return st, err
		}
		var opts []metamodel.Option
		for _, r := range res.Rows {
			s := clauseSetRow{Types: crud.Str(r[2])}
			if s.fits(c.Type) {
				opts = append(opts, metamodel.Option{Value: crud.Str(r[0]), Label: crud.Str(r[1])})
			}
		}
		fs := metamodel.FieldState{Options: opts}
		if len(opts) > 0 && req.Values["cl_set"] == "" {
			fs.Value = &opts[0].Value
		}
		st.Fields["cl_set"] = fs
		switch {
		case !m.clausesEnabled(ctx, c.CompanyCode, c.Type):
			st.Message = "Die Vertragsart " + c.Type + " hat keine Vertragsklauseln (Einstellungen → Vertragsarten)."
		case len(opts) == 0:
			st.Message = "Kein aktives Klauselset für die Vertragsart " + c.Type + " (Einstellungen → Klauselsets)."
		default:
			st.Message = "Die Bedingungen der Klauseln werden jetzt ausgewertet; danach ändert nur „Vertragsklauseln erneuern“ die Kopie."
		}
	case "clauseDocument":
		opts := reportapi.Templates(ctx, m.services, usageContractDocument)
		fs := metamodel.FieldState{Options: opts}
		if len(opts) > 0 && req.Values["cl_template"] == "" {
			v := opts[0].Value
			if set, _ := m.copyState(ctx, c); set != "" {
				if s, err := m.clauseSetOf(ctx, c.CompanyCode, set); err == nil && s.Template != "" {
					for _, o := range opts {
						if o.Value == s.Template || strings.HasPrefix(o.Value, s.Template+"|") {
							v = o.Value
						}
					}
				}
			}
			fs.Value = &v
		}
		st.Fields["cl_template"] = fs
		if len(opts) == 0 {
			st.Message = "Keine Vorlage mit der Verwendung " + usageContractDocument + " (Vorlagen → „Beispiele anlegen …“ legt CONTRACT_DOCUMENT an)."
		} else {
			st.Message = "Das PDF lässt sich beliebig oft erzeugen; Platzhalter gelten mit den aktuellen Vertragsdaten."
		}
	}
	return st, nil
}
