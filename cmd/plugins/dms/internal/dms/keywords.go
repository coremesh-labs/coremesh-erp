package dms

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Stichworte (seit 0.27.0, DmsKeyword): Ein Begriff im erkannten Text spricht für
// eine Dokumentart und/oder einen Bezug – „Lohnsteuerbescheinigung“ → WAGE_TAX und
// Asset PS001 (Bestandteil Steuern der Person). Die Erkennungsregeln kw_doc_type und
// kw_ref des Katalogs DmsHint lesen die Stichworte; der Treffer mit den meisten
// Punkten zählt je Kandidat, längere Begriffe bekommen sinnvollerweise mehr Punkte.
// Mit Weiterleitungsziel (Regel kw_forward → Variable forward) gehört das Dokument in ein
// anderes System, z. B. VuV-Unterlagen im privaten Eingang.

func (m *Module) keyword() *crud.Entity {
	return &crud.Entity{
		Object: keywordObject, Title: "Stichworte (Erkennen)", Icon: "icon-tag", Table: "dms__keyword", Section: secSettings,
		Keys: []string{"company_code", "code"}, Order: "company_code, code", StatusField: "is_active", TitleField: "phrase",
		Filters: []string{"company_code", "doc_type", "ref"}, Search: []string{"code", "phrase", "note"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "phrase", Label: "Begriff im Text (ohne Groß-/Kleinschreibung und Umlaute)", Short: "Begriff", Type: tText, Required: true, Listable: true},
			{Key: "doc_type", Label: "Dokumentart", Type: tText, Listable: true,
				Lookup: &metamodel.Lookup{Object: docTypeObject, ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "ref_object", Label: "Bezug: Object (z. B. Asset)", Type: tText, Group: "Bezug"},
			{Key: "ref_key", Label: "Bezug: Schlüssel (z. B. PS001)", Type: tText, Group: "Bezug"},
			{Key: "ref", Label: "Bezug", Type: tText, Listable: true, ReadOnly: true},
			{Key: "forward", Label: "Weiterleiten an (Weiterleitungsziel, z. B. Dokumente eines anderen Systems)", Short: "Weiterleiten", Type: tText, Listable: true,
				Lookup: &metamodel.Lookup{Object: forwardObject, ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "points", Label: "Punkte", Type: tNum, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access:   &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: m.checkKeyword,
	}
}

func (m *Module) checkKeyword(ctx context.Context, rec, _ crud.Record) error {
	if err := checkCode(rec, "code", "Schlüssel"); err != nil {
		return err
	}
	cc := crud.Str(rec["company_code"])
	if _, err := m.companyConfig(ctx, cc); err != nil {
		return err
	}
	if len(strings.TrimSpace(foldText(crud.Str(rec["phrase"])))) < 3 {
		return crud.Invalid("Begriff: mindestens drei Buchstaben oder Ziffern")
	}
	rec["phrase"] = strings.TrimSpace(crud.Str(rec["phrase"]))
	rec["doc_type"] = nilIfEmpty(trimUpper(rec["doc_type"]))
	if dt := crud.Str(rec["doc_type"]); dt != "" {
		if _, err := m.docTypeOf(ctx, dt); err != nil {
			return err
		}
	}
	rec["forward"] = nilIfEmpty(trimUpper(rec["forward"]))
	if f := crud.Str(rec["forward"]); f != "" {
		res, err := m.db.Query(ctx, `SELECT 1 FROM dms__forward WHERE company_code = ? AND code = ? AND is_active = ?`, cc, f, true)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return crud.Invalid("Weiterleitungsziel %s gibt es im Buchungskreis %s nicht", f, cc)
		}
	}
	obj, key := strings.TrimSpace(crud.Str(rec["ref_object"])), strings.TrimSpace(crud.Str(rec["ref_key"]))
	rec["ref_object"], rec["ref_key"], rec["ref"] = nilIfEmpty(obj), nilIfEmpty(key), nil
	switch {
	case obj == "" && key == "":
		if rec["doc_type"] == nil && rec["forward"] == nil {
			return crud.Invalid("Dokumentart, Bezug oder Weiterleitungsziel angeben")
		}
	case obj == "" || key == "":
		return crud.Invalid("Bezug: Object und Schlüssel angeben")
	default:
		v, err := m.checkParamValue(ctx, cc, obj, key)
		if err != nil {
			return err
		}
		rec["ref"] = obj + ":" + v
	}
	if crud.Str(rec["points"]) == "" {
		rec["points"] = 3
	}
	if toInt(rec["points"]) <= 0 {
		return crud.Invalid("Punkte: größer als 0")
	}
	return nil
}
