package inspection

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Kataloge je Buchungskreis. Beim ersten Protokoll im Buchungskreis (oder mit
// inspection:setup-company) entstehen die Vorschlagswerte, soweit sie fehlen.

var (
	codeRe   = regexp.MustCompile(`^[A-Z0-9_]{1,12}$`)
	prefixRe = regexp.MustCompile(`^[A-Za-z]{1,4}$`)
)

// Art eines Prüfpunkts: Haken, Anzahl (Schlüssel) oder Text (neue Anschrift).
var kindOptions = []metamodel.Option{
	{Value: "CHECK", Label: "erledigt ja/nein"}, {Value: "COUNT", Label: "Anzahl"}, {Value: "TEXT", Label: "Text"},
}

// Herkunft eines Teilnehmers beim Übernehmen.
var sourceOptions = []metamodel.Option{
	{Value: "CONTRACT", Label: "Partner des Vertrags (Mieter)"}, {Value: "COMPANY", Label: "Partner des Buchungskreises (Vermieter, Verwaltung)"},
}

func (m *Module) catalogEntities() []*crud.Entity {
	out := make([]*crud.Entity, 0, len(catalogTables))
	for _, c := range catalogTables {
		e := m.catalogEntity(c)
		m.withLabels(e)
		out = append(out, e)
	}
	return out
}

func (m *Module) catalogEntity(c catalogTable) *crud.Entity {
	fields := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
		{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
	}
	switch c.Table {
	case "protocol_type":
		fields = append(fields,
			crud.Field{Key: "id_prefix", Label: "Kürzel der Nummern (z. B. UA → UA-2026-0001)", Short: "Kürzel", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "contract_types", Label: "Vertragsarten (durch Komma, z. B. MV,GM; leer = ohne Vertrag)", Short: "Verträge", Type: tText, Listable: true},
			crud.Field{Key: "with_rooms", Label: "Räume und Bauteile aus dem Raumbuch", Short: "Räume", Type: tBool, Listable: true},
			crud.Field{Key: "with_meters", Label: "Zählerstände aus den technischen Objekten", Short: "Zähler", Type: tBool, Listable: true},
			crud.Field{Key: "reading_type", Label: "Ableseart der Zählerstände beim Abschluss (z. B. HANDOVER; leer = nicht buchen)", Short: "Ableseart", Type: tText},
			crud.Field{Key: "template", Label: "Vorlage (Code|Sprache, Verwendung PROTOCOL_SHEET; leer = erste der Verwendung)", Short: "Vorlage", Type: tText},
			crud.Field{Key: "doc_type", Label: "Dokumentart im DMS (z. B. REPORT; leer = nicht ablegen)", Short: "Dokumentart", Type: tText},
			crud.Field{Key: "defect_process_type", Label: "Vorgangsart für Mängel (z. B. SCHADEN; leer = kein Vorgang)", Short: "Mängel als", Type: tText},
			crud.Field{Key: "intro", Label: "Einleitung im Protokoll", Type: tArea},
			crud.Field{Key: "closing", Label: "Schluss vor den Unterschriften", Type: tArea})
	case "condition":
		fields = append(fields, crud.Field{Key: "is_defect", Label: "Mangel (Mängelliste, Vorgang)", Short: "Mangel", Type: tBool, Listable: true})
	case "checklist_item":
		fields = append(fields,
			crud.Field{Key: "kind", Label: "Erfassung", Type: tSel, Listable: true, Options: kindOptions},
			crud.Field{Key: "protocol_types", Label: "Für Protokollarten (durch Komma; leer = alle)", Short: "Arten", Type: tText, Listable: true},
			crud.Field{Key: "hint", Label: "Hinweis im Formular", Short: "Hinweis", Type: tText})
	case "participant_role":
		fields = append(fields,
			crud.Field{Key: "source", Label: "Beim Übernehmen vorschlagen", Short: "Vorschlag", Type: tSel, Listable: true, Options: sourceOptions},
			crud.Field{Key: "signs", Label: "Unterschreibt", Type: tBool, Listable: true})
	}
	fields = append(fields,
		crud.Field{Key: "sort_order", Label: "Reihenfolge", Short: "Reihenf.", Type: tNum, Listable: true},
		crud.Field{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true})
	return &crud.Entity{
		Object: c.Object, Customizing: true, Title: c.Title, Icon: "icon-tag", Table: "inspection__" + c.Table, Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields:      fields,
		Access:      &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: checkWrite(c.Object),
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			return m.checkCatalog(ctx, c.Table, rec, old)
		},
	}
}

func (m *Module) checkCatalog(ctx context.Context, table string, rec, old crud.Record) error {
	rec["code"] = trimUpper(rec["code"])
	if !codeRe.MatchString(crud.Str(rec["code"])) {
		return crud.Invalid("Schlüssel %q: 1–12 Großbuchstaben, Ziffern oder _", crud.Str(rec["code"]))
	}
	switch table {
	case "protocol_type":
		p := strings.ToUpper(strings.TrimSpace(crud.Str(rec["id_prefix"])))
		if !prefixRe.MatchString(p) {
			return crud.Invalid("Kürzel %q: 1–4 Buchstaben", p)
		}
		rec["id_prefix"] = p
		for _, k := range []string{"reading_type", "doc_type", "defect_process_type"} {
			rec[k] = nilIfEmpty(trimUpper(rec[k]))
		}
		rec["contract_types"] = nilIfEmpty(strings.Join(splitCodes(crud.Str(rec["contract_types"])), ","))
		rec["template"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["template"])))
	case "checklist_item":
		if k := trimUpper(rec["kind"]); k == "" {
			rec["kind"] = "CHECK"
		} else {
			rec["kind"] = k
		}
		if !validOption(kindOptions, crud.Str(rec["kind"])) {
			return crud.Invalid("Erfassung %q: CHECK, COUNT oder TEXT", crud.Str(rec["kind"]))
		}
		types := splitCodes(crud.Str(rec["protocol_types"]))
		for _, t := range types {
			if ok, err := m.exists(ctx, "protocol_type", crud.Str(rec["company_code"]), t); err != nil {
				return err
			} else if !ok {
				return crud.Invalid("Protokollart %s gibt es nicht", t)
			}
		}
		rec["protocol_types"] = nilIfEmpty(strings.Join(types, ","))
	case "participant_role":
		rec["source"] = nilIfEmpty(trimUpper(rec["source"]))
		if s := crud.Str(rec["source"]); s != "" && !validOption(sourceOptions, s) {
			return crud.Invalid("Vorschlag %q: CONTRACT oder COMPANY", s)
		}
	}
	return nil
}

func validOption(opts []metamodel.Option, v string) bool {
	for _, o := range opts {
		if o.Value == v {
			return true
		}
	}
	return false
}

func (m *Module) exists(ctx context.Context, table, cc, code string) (bool, error) {
	v, err := m.one(ctx, "SELECT 1 FROM inspection__"+table+" WHERE company_code = ? AND code = ?", cc, code)
	return v != "", err
}

// catalogDefaults: Vorschlagswerte je Katalog (im Buchungskreis änderbar).
var catalogDefaults = map[string][]map[string]any{
	"protocol_type": {
		{"code": "MOVE_IN", "name": "Wohnungsübergabe Einzug", "id_prefix": "UE", "contract_types": "MV,GM", "reading_type": "HANDOVER",
			"doc_type": "REPORT", "defect_process_type": "SCHADEN", "with_rooms": true, "with_meters": true,
			"intro": "Die Mieträume wurden heute gemeinsam besichtigt und in dem nachstehend beschriebenen Zustand übergeben.",
			"closing": "Der Mieter hat die aufgeführten Schlüssel erhalten. Die Zählerstände wurden gemeinsam abgelesen. " +
				"Mit ihrer Unterschrift bestätigen die Beteiligten den festgestellten Zustand; aufgeführte Mängel beseitigt der Vermieter."},
		{"code": "MOVE_OUT", "name": "Wohnungsübergabe Auszug", "id_prefix": "UA", "contract_types": "MV,GM", "reading_type": "HANDOVER",
			"doc_type": "REPORT", "defect_process_type": "SCHADEN", "with_rooms": true, "with_meters": true,
			"intro": "Die Mieträume wurden heute gemeinsam besichtigt und in dem nachstehend beschriebenen Zustand zurückgegeben.",
			"closing": "Der Vermieter hat die aufgeführten Schlüssel zurückerhalten. Die Zählerstände wurden gemeinsam abgelesen. " +
				"Mit ihrer Unterschrift bestätigen die Beteiligten den festgestellten Zustand. Weitere Ansprüche bleiben vorbehalten."},
		{"code": "INSPECTION", "name": "Begehung", "id_prefix": "BG", "doc_type": "REPORT", "defect_process_type": "SCHADEN", "with_rooms": true,
			"intro": "Bei der Begehung wurde folgender Zustand festgestellt."},
	},
	"condition": {
		{"code": "OK", "name": "in Ordnung"},
		{"code": "USED", "name": "Gebrauchsspuren"},
		{"code": "DIRTY", "name": "verschmutzt", "is_defect": true},
		{"code": "DEFECT", "name": "beschädigt / defekt", "is_defect": true},
		{"code": "MISSING", "name": "fehlt", "is_defect": true},
		{"code": "NA", "name": "nicht vorhanden"},
	},
	"checklist_item": {
		{"code": "KEY_HOUSE", "name": "Haustürschlüssel", "kind": "COUNT", "protocol_types": "MOVE_IN,MOVE_OUT"},
		{"code": "KEY_FLAT", "name": "Wohnungsschlüssel", "kind": "COUNT", "protocol_types": "MOVE_IN,MOVE_OUT"},
		{"code": "KEY_MAIL", "name": "Briefkastenschlüssel", "kind": "COUNT", "protocol_types": "MOVE_IN,MOVE_OUT"},
		{"code": "KEY_CELLAR", "name": "Kellerschlüssel", "kind": "COUNT", "protocol_types": "MOVE_IN,MOVE_OUT"},
		{"code": "SMOKE", "name": "Rauchwarnmelder vorhanden und geprüft", "kind": "CHECK"},
		{"code": "CLEAN", "name": "Wohnung besenrein", "kind": "CHECK", "protocol_types": "MOVE_OUT"},
		{"code": "CELLAR", "name": "Keller / Abstellraum geräumt", "kind": "CHECK", "protocol_types": "MOVE_OUT"},
		{"code": "NAMEPLATE", "name": "Namensschilder an Klingel und Briefkasten", "kind": "CHECK", "protocol_types": "MOVE_IN,MOVE_OUT"},
		{"code": "PAPERS", "name": "Hausordnung und Bedienungsanleitungen übergeben", "kind": "CHECK", "protocol_types": "MOVE_IN"},
		{"code": "ADDRESS", "name": "Neue Anschrift des Mieters", "kind": "TEXT", "protocol_types": "MOVE_OUT"},
	},
	"participant_role": {
		{"code": "TENANT", "name": "Mieter", "source": "CONTRACT", "signs": true},
		{"code": "LANDLORD", "name": "Vermieter / Verwaltung", "source": "COMPANY", "signs": true},
		{"code": "SUCCESSOR", "name": "Nachmieter", "signs": true},
		{"code": "WITNESS", "name": "Zeuge", "signs": true},
		{"code": "CARETAKER", "name": "Hausmeister"},
	},
}

// catalogColumns: Zusatzspalten der Vorschlagswerte.
var catalogColumns = []string{"code", "name", "id_prefix", "contract_types", "reading_type", "template", "doc_type", "defect_process_type",
	"with_rooms", "with_meters", "intro", "closing", "is_defect", "kind", "protocol_types", "hint", "source", "signs"}

type setupKey struct{}

// setupCatalogs legt fehlende Vorschlagswerte im Buchungskreis an. Ein Katalog,
// der schon Einträge hat, bekommt sie nur auf ausdrücklichen Wunsch (setupCompany).
func (m *Module) setupCatalogs(ctx context.Context, cc string) (int, error) {
	explicit := ctx.Value(setupKey{}) != nil
	n := 0
	for _, c := range catalogTables {
		if !explicit {
			if has, err := m.one(ctx, "SELECT 1 FROM inspection__"+c.Table+" WHERE company_code = ? LIMIT 1", cc); err != nil {
				return 0, err
			} else if has != "" {
				continue
			}
		}
		for i, row := range catalogDefaults[c.Table] {
			ok, err := m.exists(ctx, c.Table, cc, crud.Str(row["code"]))
			if err != nil {
				return 0, err
			}
			if ok {
				if explicit {
					k, err := m.fillDefaults(ctx, c.Table, cc, row)
					if err != nil {
						return 0, err
					}
					n += k
				}
				continue
			}
			cols := []string{"company_code", "sort_order", "is_active"}
			args := []any{cc, (i + 1) * 10, true}
			for _, k := range catalogColumns {
				if v, ok := row[k]; ok {
					cols, args = append(cols, k), append(args, v)
				}
			}
			for _, k := range boolColumns(c.Table) {
				if _, ok := row[k]; !ok {
					cols, args = append(cols, k), append(args, false)
				}
			}
			if _, err := m.db.Exec(ctx, "INSERT INTO inspection__"+c.Table+" ("+strings.Join(cols, ", ")+") VALUES ("+
				strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")+")", args...); err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, nil
}

// boolColumns: Kennzeichen, die ein Vorschlag ohne Angabe ausdrücklich auf
// „nein“ setzt (die Tabelle hat teils „ja“ als Standard).
func boolColumns(table string) []string {
	switch table {
	case "protocol_type":
		return []string{"with_rooms", "with_meters"}
	case "participant_role":
		return []string{"signs"}
	}
	return nil
}

// fillDefaults: leere Textspalten eines vorhandenen Vorschlagseintrags ergänzen.
func (m *Module) fillDefaults(ctx context.Context, table, cc string, row map[string]any) (int, error) {
	n := 0
	for _, k := range catalogColumns {
		v, ok := row[k]
		if !ok || k == "code" || k == "name" {
			continue
		}
		if _, isText := v.(string); !isText {
			continue
		}
		res, err := m.db.Exec(ctx, "UPDATE inspection__"+table+" SET "+k+" = ? WHERE company_code = ? AND code = ? AND ("+k+" IS NULL OR "+k+" = '')", v, cc, row["code"])
		if err != nil {
			return 0, err
		}
		if res.RowsAffected > 0 {
			n++
		}
	}
	return n, nil
}

// setupCompanyAction: InspectionSetup.setupCompany {company} (Konsole inspection:setup-company).
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
	if err := requireWrite(ctx, "InspectionType", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	var n int
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		var err error
		n, err = m.setupCatalogs(context.WithValue(ctx, setupKey{}, true), cc)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "created": n,
		"message": fmt.Sprintf("Buchungskreis %s: %d Katalogeinträge der Begehungen und Protokolle angelegt bzw. ergänzt", cc, n)}}, nil
}
