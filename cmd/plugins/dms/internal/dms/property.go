package dms

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Objekt-Ebene im Archiv (DmsCompany.object_dir = PROPERTY): unter der Ablage
// ein Verzeichnis je Objekt (Gebäude bzw. Wirtschaftseinheit) –
// „…/3.0.1 Grundsteuerbescheide/ROBro1 Brockenstraße 1/…“. Das Objekt
// kommt aus den Bezügen des Dokuments: Gebäude oder Wirtschaftseinheit direkt,
// sonst das Gebäude eines Mietobjekts, das Hauptobjekt eines Vertrags, das
// Objekt einer Eingangsrechnung, eines Angebots oder Auftrags (Felder object_type,
// object_id), sonst das Objekt des Vertrags eines Datensatzes (Feld contract_id, z. B.
// Wirtschaftsplan, seit 0.23.0). Ohne Objekt: Verzeichnis object_dir_fallback.

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

// objectLevel: Verzeichnis-Ebene unter der Ablage (nil = keine).
func (m *Module) objectLevel(ctx context.Context, cfg *companyConfig, d *docRow) *Level {
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
