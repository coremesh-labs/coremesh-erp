package dms

import (
	"context"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Ablage über Objekte (seit 0.26.0, Plugin asset): Ein Dokument bezieht sich auf
// ein Objekt, z. B. den Bestandteil „Steuern“ der Person P001. Die Ablagefindung
// der Ablagen im Buchungskreis kennt Objektarten (asset_types) und Dokumentarten
// (doc_types): Bestandteil der Art PS (Steuern) × Bescheid → 8.1.0 Steuerbescheide.
// Die Objekt-Ebene im Archiv ist das oberste Objekt (die Person), nicht der Bestandteil.

// assetRule: Ablagefindung über Objekt- und Dokumentarten.
type assetRule struct {
	Folder          string
	Types, DocTypes []string
}

// score: Objektart muss passen (1), Dokumentart, wenn gesetzt, zusätzlich (2).
func (r assetRule) score(assetType, docType string) int {
	if len(r.Types) == 0 || !contains(r.Types, assetType) {
		return 0
	}
	if len(r.DocTypes) == 0 {
		return 1
	}
	if !contains(r.DocTypes, docType) {
		return 0
	}
	return 3
}

// overlaps: beide Regeln träfen dasselbe Dokument gleich genau.
func (r assetRule) overlaps(o assetRule) bool {
	meet := func(a, b []string) bool {
		for _, x := range a {
			if contains(b, x) {
				return true
			}
		}
		return false
	}
	if !meet(r.Types, o.Types) {
		return false
	}
	if len(r.DocTypes) == 0 || len(o.DocTypes) == 0 {
		return len(r.DocTypes) == len(o.DocTypes)
	}
	return meet(r.DocTypes, o.DocTypes)
}

func (r assetRule) text() string {
	s := "Objektart " + strings.Join(r.Types, ", ")
	if len(r.DocTypes) > 0 {
		s += ", Dokumentart " + strings.Join(r.DocTypes, ", ")
	}
	return s
}

func (m *Module) assetRules(ctx context.Context, cc string) ([]assetRule, error) {
	res, err := m.db.Query(ctx, `SELECT number, asset_types, doc_types FROM dms__account_company
		WHERE company_code = ? AND asset_types <> '' AND is_blocked = ?`, cc, false)
	if err != nil {
		return nil, err
	}
	var out []assetRule
	for _, r := range res.Rows {
		out = append(out, assetRule{Folder: crud.Str(r[0]), Types: splitList(strings.ToUpper(crud.Str(r[1]))),
			DocTypes: splitList(strings.ToUpper(crud.Str(r[2])))})
	}
	return out, nil
}

// assetRec: Objekt aus dem Plugin asset (nil = nicht lesbar).
func (m *Module) assetRec(ctx context.Context, cc, key string) map[string]any {
	if !strings.HasPrefix(key, cc+"|") {
		key = cc + "|" + key
	}
	resp, err := m.services.Call(ctx, "Asset", "get", map[string]any{"id": key})
	if err != nil {
		return nil
	}
	var rec map[string]any
	if sdk.Decode(resp.Payload, &rec) != nil {
		return nil
	}
	return rec
}

// folderFromAssets: Ablage über die bezogenen Objekte des Dokuments. Liefert die Ablage
// und die Begründung; mehrere gleich genaue Ablagen: Fehler.
func (m *Module) folderFromAssets(ctx context.Context, d *docRow) (string, string, error) {
	refs, err := m.activeRefs(ctx, d.ID)
	if err != nil {
		return "", "", err
	}
	var rules []assetRule
	best, top := "", 0
	var tied []string
	why := ""
	for _, r := range refs {
		if r.Object != "Asset" {
			continue
		}
		if rules == nil {
			if rules, err = m.assetRules(ctx, d.CC); err != nil || len(rules) == 0 {
				return "", "", err
			}
		}
		rec := m.assetRec(ctx, d.CC, r.Key)
		if rec == nil {
			continue
		}
		typ := strings.ToUpper(crud.Str(rec["asset_type"]))
		for _, rule := range rules {
			switch n := rule.score(typ, strings.ToUpper(d.DocType)); {
			case n > top:
				best, top, tied, why = rule.Folder, n, []string{rule.Folder}, rule.text()
			case n == top && n > 0 && !contains(tied, rule.Folder):
				tied = append(tied, rule.Folder)
			}
		}
	}
	if len(tied) > 1 {
		sort.Strings(tied)
		return "", "", crud.Invalid("Objekte passen gleich gut zu mehreren Ablagen (%s)", strings.Join(tied, ", "))
	}
	return best, why, nil
}

// assetRoot: oberstes Objekt eines Objekts zum Dokumentdatum (Bestandteil → Person);
// ohne Zeitscheibe zum Datum die jüngste.
func (m *Module) assetRoot(ctx context.Context, cc, key, date string) *property {
	id := shortKey(key, cc)
	seen := map[string]bool{}
	for depth := 0; depth < 8 && !seen[id]; depth++ {
		seen[id] = true
		resp, err := m.services.Call(ctx, "AssetPart", "list", map[string]any{"query": map[string]any{"company_code": cc, "part_id": id}})
		if err != nil {
			break
		}
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if sdk.Decode(resp.Payload, &list) != nil {
			break
		}
		parent, latest := "", ""
		for _, it := range list.Items {
			if crud.Str(it["part_id"]) != id {
				continue
			}
			from, to := dateOf(it["valid_from"]), dateOf(it["valid_to"])
			if date != "" && from <= date && (to == "" || to >= date) {
				parent = crud.Str(it["asset_id"])
				break
			}
			if from > latest {
				latest, parent = from, crud.Str(it["asset_id"])
			}
		}
		if parent == "" {
			break
		}
		id = parent
	}
	rec := m.assetRec(ctx, cc, id)
	if rec == nil {
		return nil
	}
	return &property{Key: id, Name: crud.Str(rec["designation"])}
}

// checkAssetRule: Objekt- und Dokumentarten der Ablage im Buchungskreis – nur an Ablagen der
// untersten Ebene, Dokumentarten nur mit Objektarten, keine gleiche Regel an zwei Ablagen.
func (m *Module) checkAssetRule(ctx context.Context, cfg *companyConfig, rec crud.Record) error {
	r := assetRule{Folder: crud.Str(rec["number"]), Types: splitList(strings.ToUpper(crud.Str(rec["asset_types"]))),
		DocTypes: splitList(strings.ToUpper(crud.Str(rec["doc_types"])))}
	rec["asset_types"], rec["doc_types"] = strings.Join(r.Types, ", "), strings.Join(r.DocTypes, ", ")
	if len(r.Types) == 0 {
		if len(r.DocTypes) > 0 {
			return crud.Invalid("Dokumentarten der Ablagefindung nur zusammen mit Objektarten")
		}
		return nil
	}
	if leaf, err := m.isLeaf(ctx, cfg.Plan, r.Folder); err != nil {
		return err
	} else if !leaf {
		return crud.Invalid("Ablagefindung (Objektarten) nur an Ablagen der untersten Ebene")
	}
	for _, dt := range r.DocTypes {
		if _, err := m.docTypeOf(ctx, dt); err != nil {
			return crud.Invalid("Dokumentart %s gibt es nicht", dt)
		}
	}
	rules, err := m.assetRules(ctx, cfg.CC)
	if err != nil {
		return err
	}
	for _, o := range rules {
		if o.Folder != r.Folder && o.overlaps(r) {
			return crud.Invalid("Gleiche Ablagefindung wie Ablage %s (%s) – Objektart oder Dokumentart unterscheiden", o.Folder, o.text())
		}
	}
	return nil
}
