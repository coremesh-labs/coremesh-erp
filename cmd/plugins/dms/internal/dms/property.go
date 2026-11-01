package dms

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Objekt-Ebene im Archiv (DmsCompany.object_dir = PROPERTY): unter der Ablage
// ein Verzeichnis je Objekt (Gebäude bzw. Wirtschaftseinheit) –
// „…/3.0.1 Grundsteuerbescheide/ROBro1 Brockenstraße 1/…“. Das Objekt
// kommt aus den Bezügen des Dokuments: Gebäude oder Wirtschaftseinheit direkt,
// sonst das Gebäude eines Mietobjekts, das Hauptobjekt eines Vertrags, das
// Objekt einer Eingangsrechnung, eines Angebots oder Auftrags (Felder object_type,
// object_id), sonst das Objekt des Vertrags eines Datensatzes (Feld contract_id, z. B.
// Wirtschaftsplan, seit 0.23.0), seit 0.26.0 das oberste Objekt eines Objekts aus dem
// Plugin asset (Bestandteil → Person). Ohne Objekt: Verzeichnis object_dir_fallback.

// property ist das Objekt eines Dokuments.
type property struct{ Key, Name string }

// propertyOf: Objekt des Dokuments aus seinen Bezügen (nil = keins).
func (m *Module) propertyOf(ctx context.Context, d *docRow) *property {
	refs, err := m.activeRefs(ctx, d.ID)
	if err != nil {
		return nil
	}
	// Erst direkte Objekte, dann abgeleitete.
	for _, pass := range []bool{true, false} {
		for _, r := range refs {
			direct := r.Object == "Building" || r.Object == "BusinessEntity"
			if direct != pass {
				continue
			}
			if r.Object == "Asset" && !pass {
				// seit 0.26.0: oberstes Objekt zum Dokumentdatum (Bestandteil → Person)
				if p := m.assetRoot(ctx, d.CC, r.Key, d.DocDate); p != nil {
					return p
				}
				continue
			}
			if p := m.propertyFrom(ctx, d.CC, r.Object, r.Key, 0); p != nil {
				return p
			}
		}
	}
	return nil
}

// propertyFrom: Objekt zu einem Bezug (depth begrenzt die Verkettung).
func (m *Module) propertyFrom(ctx context.Context, cc, object, key string, depth int) *property {
	if depth > 3 || key == "" {
		return nil
	}
	if !strings.HasPrefix(key, cc+"|") && object != "JournalDraft" {
		key = cc + "|" + key
	}
	get := func(obj, id string) map[string]any {
		resp, err := m.services.Call(ctx, obj, "get", map[string]any{"id": id})
		if err != nil {
			return nil
		}
		var rec map[string]any
		if sdk.Decode(resp.Payload, &rec) != nil {
			return nil
		}
		return rec
	}
	switch object {
	case "Building", "BusinessEntity":
		rec := get(object, key)
		if rec == nil {
			return nil
		}
		return &property{Key: shortKey(key, cc), Name: crud.Str(rec["designation"])}
	case "Asset":
		return m.assetRoot(ctx, cc, key, "")
	case "RentObject":
		rec := get(object, key)
		if rec == nil || crud.Str(rec["building_id"]) == "" {
			return nil
		}
		return m.propertyFrom(ctx, cc, "Building", crud.Str(rec["building_id"]), depth+1)
	case "Contract":
		contract := shortKey(key, cc)
		resp, err := m.services.Call(ctx, "ContractObject", "list", map[string]any{"query": map[string]any{"company_code": cc, "contract_id": contract}})
		if err != nil {
			return nil
		}
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if sdk.Decode(resp.Payload, &list) != nil {
			return nil
		}
		var first *property
		for _, it := range list.Items {
			if crud.Str(it["contract_id"]) != contract {
				continue
			}
			p := m.propertyFrom(ctx, cc, crud.Str(it["object_type"]), crud.Str(it["object_id"]), depth+1)
			if p != nil && crud.AsBool(it["is_main"]) {
				return p
			}
			if first == nil {
				first = p
			}
		}
		return first
	}
	// Eingangsrechnung, Angebot, Auftrag … (seit 0.19.0 jedes Object mit object_type/object_id),
	// sonst der Vertrag des Datensatzes (seit 0.23.0 Feld contract_id, z. B. Wirtschaftsplan)
	rec := get(object, key)
	if rec == nil {
		return nil
	}
	if crud.Str(rec["object_type"]) != "" {
		return m.propertyFrom(ctx, cc, crud.Str(rec["object_type"]), crud.Str(rec["object_id"]), depth+1)
	}
	if c := crud.Str(rec["contract_id"]); c != "" {
		return m.propertyFrom(ctx, cc, "Contract", c, depth+1)
	}
	return nil
}

// objectPart: Wert für {object} – Schlüssel des ersten Bezugs; bei einem
// zusammengesetzten Schlüssel (z. B. Wirtschaftsplan Vertrag|Zeitraum) der Vertrag
// des Datensatzes (Feld contract_id, seit 0.23.0).
func (m *Module) objectPart(ctx context.Context, cc string, r refRow) string {
	key := shortKey(r.Key, cc)
	if !strings.Contains(strings.TrimPrefix(r.Key, cc+"|"), "|") {
		return key
	}
	id := r.Key
	if !strings.HasPrefix(id, cc+"|") {
		id = cc + "|" + id
	}
	if resp, err := m.services.Call(ctx, r.Object, "get", map[string]any{"id": id}); err == nil {
		var rec map[string]any
		if sdk.Decode(resp.Payload, &rec) == nil {
			if c := crud.Str(rec["contract_id"]); c != "" {
				return c
			}
		}
	}
	return key
}

// objectLevel: Verzeichnis-Ebene unter der Ablage (nil = keine) – in einer Ablage
// mit Ordner je Bezug der Ordner des Bezugs, sonst die Objekt-Ebene.
func (m *Module) objectLevel(ctx context.Context, cfg *companyConfig, d *docRow) *Level {
	if lvl := m.folderLevel(ctx, cfg, d); lvl != nil {
		return lvl
	}
	if cfg.ObjectDir != "PROPERTY" {
		return nil
	}
	if p := m.propertyOf(ctx, d); p != nil {
		return &Level{Number: p.Key, Name: p.Name}
	}
	return &Level{Name: firstOf(cfg.ObjectDirFallback, "Allgemein")}
}

// propertyPart: Wert für {property} – genau der Schlüssel der Objekt-Ebene
// (ohne Objekt das Ersatzverzeichnis), leer ohne Objekt-Ebene.
func (m *Module) propertyPart(ctx context.Context, cfg *companyConfig, d *docRow) string {
	lvl := m.objectLevel(ctx, cfg, d)
	if lvl == nil {
		return ""
	}
	return firstOf(lvl.Number, lvl.Name)
}

// folderLevel (seit 0.29.0): Ordner „Schlüssel Feld“ des Bezugs, wenn die Ablage
// einen Ordner je Bezug hat (z. B. je Vorgang „EE-2026-0001 HGA 2025“); nil sonst
// oder ohne einen solchen Bezug.
func (m *Module) folderLevel(ctx context.Context, cfg *companyConfig, d *docRow) *Level {
	if d.Account == "" {
		return nil
	}
	info, err := m.accountPath(ctx, cfg.Plan, d.Account)
	if err != nil || info.FolderObject == "" {
		return nil
	}
	refs, err := m.activeRefs(ctx, d.ID)
	if err != nil {
		return nil
	}
	for _, r := range refs {
		if r.Object != info.FolderObject {
			continue
		}
		lvl := &Level{Number: shortKey(r.Key, d.CC)}
		if info.FolderField != "" {
			id := r.Key
			if !strings.HasPrefix(id, d.CC+"|") {
				id = d.CC + "|" + id
			}
			if resp, err := m.services.Call(ctx, r.Object, "get", map[string]any{"id": id}); err == nil {
				var rec map[string]any
				if sdk.Decode(resp.Payload, &rec) == nil {
					lvl.Name = crud.Str(rec[info.FolderField])
				}
			}
		}
		return lvl
	}
	return nil
}

// folderAccount (seit 0.29.0): Ablage mit Ordner je Bezug für das Object im
// Buchungskreis (aktiv, aktiviert, nicht gesperrt; "" = keine).
func (m *Module) folderAccount(ctx context.Context, cc, object string) string {
	res, err := m.db.Query(ctx, `SELECT a.number FROM dms__account a JOIN dms__company c ON c.plan = a.plan
		JOIN dms__account_company ac ON ac.company_code = c.company_code AND ac.number = a.number
		WHERE c.company_code = ? AND a.folder_object = ? AND a.is_active = ? AND COALESCE(ac.is_blocked, ?) = ?
		ORDER BY a.sort_key`, cc, object, true, false, false)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	return crud.Str(res.Rows[0][0])
}

// Objekt am Dokument (seit 0.30.0): dms__document.property hält den Schlüssel des
// Objekts aus den Bezügen (wie die Objekt-Ebene im Archiv), dms__property Schlüssel und
// Bezeichnung für die Auswahl „Objekt“ der Dokumentliste. Beides entsteht mit dem
// Suchtext (nach Workflow, Ablegen, Änderung der Bezüge, Meldung anderer Module) und
// beim Start für Dokumente, deren Objekt noch nicht ermittelt ist.

var lookupProperty = &metamodel.Lookup{Object: propertyObject, ValueField: "code", LabelFields: []string{"name"}}

func (m *Module) propertyList() *crud.Entity {
	return &crud.Entity{
		Object: propertyObject, Title: "Objekte", Icon: "icon-home", Table: "dms__property", Section: secDocs,
		Keys: []string{"company_code", "code"}, Order: "company_code, code", TitleField: "name", ReadOnly: true,
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "code", Label: "Objekt", Type: tText, Listable: true, ReadOnly: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "dokumente", Title: "Dokumente", Relation: &metamodel.Relation{Object: docObject, ForeignKey: "property",
				Match: map[string]string{"company_code": "company_code", "property": "code"}, Columns: []string{"title", "doc_type", "file_account", "doc_date", "status"}}},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
	}
}

// setProperty: Objekt des Dokuments ermitteln und speichern ("" = keins).
func (m *Module) setProperty(ctx context.Context, d *docRow) {
	key := ""
	if p := m.propertyOf(ctx, d); p != nil && p.Key != "" {
		key = p.Key
		if _, err := m.db.Exec(ctx, `INSERT INTO dms__property (company_code, code, name) VALUES (?, ?, ?)
			ON CONFLICT (company_code, code) DO UPDATE SET name = excluded.name`, d.CC, p.Key, nilIfEmpty(p.Name)); err != nil {
			m.log.WarnContext(ctx, "Objekt nicht gespeichert", "document", d.ID, "err", err.Error())
			return
		}
	}
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET property = ? WHERE id = ?`, key, d.ID); err != nil {
		m.log.WarnContext(ctx, "Objekt nicht gespeichert", "document", d.ID, "err", err.Error())
	}
}

// setPropertyMissing: Objekt für Dokumente, bei denen es noch nicht ermittelt ist.
func (m *Module) setPropertyMissing(ctx context.Context) (int, error) {
	res, err := m.db.Query(ctx, `SELECT id FROM dms__document WHERE property IS NULL AND status IN (?, ?)`, stInbox, stFiled)
	if err != nil {
		return 0, err
	}
	for _, r := range res.Rows {
		if d, err := m.loadDoc(ctx, crud.Str(r[0])); err == nil {
			m.setProperty(ctx, d)
		}
	}
	return len(res.Rows), nil
}
