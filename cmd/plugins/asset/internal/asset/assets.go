package asset

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Sprechende IDs: <Kürzel der Objektart><Nummer, 3-stellig>, z. B. AU001 für
// das erste Auto. Ohne Eingabe vergibt das Modul die nächste freie ID; IDs sind
// je Buchungskreis eindeutig (ohne Groß-/Kleinschreibung).

var idRe = regexp.MustCompile(`^[A-Za-z0-9]{2,30}$`)

// --- Objekt -------------------------------------------------------------------------

func (m *Module) asset() *crud.Entity {
	return &crud.Entity{
		Object: "Asset", Title: "Objekte", Icon: "icon-package", Table: "asset__asset", Section: "Bestand",
		Keys: []string{"company_code", "asset_id"}, Order: "company_code, asset_type, asset_id", TitleField: "designation",
		Filters: []string{"company_code", "asset_type", "status"},
		Search:  []string{"asset_id", "designation", "identifier", "manufacturer", "model"},
		Events:  true, CompanyCodeField: "company_code", EventFields: []string{"asset_type", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "asset_type", Label: "Objektart", Short: "Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("AssetType")},
			{Key: "asset_id", Label: "Objekt (ID; leer = nächste freie, z. B. AU001)", Short: "Objekt", Type: tText, Listable: true, Immutable: true},
			{Key: "designation", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "identifier", Label: "Kennung (Kennzeichen, Rufnummer, Seriennummer, Buchungsnummer …)", Short: "Kennung", Type: tText, Listable: true},
			{Key: "manufacturer", Label: "Hersteller / Anbieter", Short: "Hersteller", Type: tText, Group: "Beschreibung"},
			{Key: "model", Label: "Modell / Typ", Short: "Modell", Type: tText, Group: "Beschreibung"},
			{Key: "street", Label: "Straße", Type: tText, Group: "Anschrift"},
			{Key: "zip", Label: "PLZ", Type: tText, Group: "Anschrift"},
			{Key: "city", Label: "Ort", Type: tText, Group: "Anschrift"},
			{Key: "country", Label: "Land", Type: tText, Group: "Anschrift"},
			{Key: "purchase_date", Label: "Angeschafft am", Type: tDate, Group: "Anschaffung"},
			{Key: "purchase_price", Label: "Kaufpreis", Type: tNum, Group: "Anschaffung"},
			{Key: "currency", Label: "Währung", Type: tText, Group: "Anschaffung"},
			{Key: "warranty_until", Label: "Garantie bis", Type: tDate, Group: "Anschaffung"},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "status", Label: "Status", Type: tText, Listable: true, Lookup: catalogLookup("AssetStatus"), Group: "Gültigkeit"},
			{Key: "valid_from", Label: "Gültig ab", Type: tDate, Listable: true, Group: "Gültigkeit"},
			{Key: "valid_to", Label: "Gültig bis", Type: tDate, Group: "Gültigkeit"},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "bestandteile", Title: "Bestandteile", Relation: &metamodel.Relation{Object: "AssetPart", ForeignKey: "asset_id",
				Match: match("asset_id", "asset_id"), Columns: []string{"part_id", "valid_from", "valid_to", "note"}}},
			{Key: "teil_von", Title: "Bestandteil von", Collapsed: true, Relation: &metamodel.Relation{Object: "AssetPart", ForeignKey: "part_id",
				Match: match("part_id", "asset_id"), Columns: []string{"asset_id", "valid_from", "valid_to"}}},
			{Key: "personen", Title: "Personen", Relation: &metamodel.Relation{Object: "AssetPerson", ForeignKey: "asset_id",
				Match: match("asset_id", "asset_id"), Columns: []string{"role_code", "partner_id", "valid_from", "valid_to"}}},
			{Key: "merkmale", Title: "Merkmale", Tags: true},
			{Key: "dokumente", Title: "Dokumente", Collapsed: true, Documents: true},
			{Key: "texte", Title: "Texte", Collapsed: true, Texts: true},
		},
		Access: access(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "Asset", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkAsset,
	}
}

func (m *Module) checkAsset(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	if old == nil {
		// Erstes Objekt im Buchungskreis: Kataloge mit Vorschlagswerten.
		if _, err := m.setupCatalogs(ctx, cc); err != nil {
			return err
		}
		rec["asset_type"] = trimUpper(rec["asset_type"])
		prefix, err := m.typePrefix(ctx, cc, crud.Str(rec["asset_type"]))
		if err != nil {
			return err
		}
		id := strings.TrimSpace(crud.Str(rec["asset_id"]))
		if id == "" {
			if id, err = m.nextID(ctx, cc, prefix); err != nil {
				return err
			}
		} else if err := m.checkNewID(ctx, cc, id); err != nil {
			return err
		}
		rec["asset_id"] = id
	}
	for _, k := range []string{"purchase_date", "warranty_until"} {
		if crud.Str(rec[k]) == "" {
			rec[k] = nil
			continue
		}
		d, err := crud.ParseDate(rec[k])
		if err != nil {
			return err
		}
		rec[k] = d
	}
	if rec["purchase_price"] != nil && crud.Str(rec["purchase_price"]) != "" && toFloat(rec["purchase_price"]) < 0 {
		return crud.Invalid("Kaufpreis darf nicht negativ sein")
	}
	rec["currency"] = nilIfEmpty(trimUpper(rec["currency"]))
	return m.checkValidity(ctx, rec)
}

// typePrefix: Kürzel der (aktiven) Objektart.
func (m *Module) typePrefix(ctx context.Context, cc, typ string) (string, error) {
	res, err := m.db.Query(ctx, "SELECT id_prefix FROM asset__asset_type WHERE company_code = ? AND code = ? AND is_active = ?", cc, typ, true)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", crud.Invalid("Objektart %q gibt es im Buchungskreis %s nicht (oder inaktiv) – Objekte → Einstellungen → Objektarten", typ, cc)
	}
	return crud.Str(res.Rows[0][0]), nil
}

func (m *Module) idTaken(ctx context.Context, cc, id string) (bool, error) {
	res, err := m.db.Query(ctx, "SELECT 1 FROM asset__asset WHERE company_code = ? AND LOWER(asset_id) = LOWER(?)", cc, id)
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}

func (m *Module) checkNewID(ctx context.Context, cc, id string) error {
	if !idRe.MatchString(id) {
		return crud.Invalid("Objekt-ID %q: 2–30 Buchstaben und Ziffern", id)
	}
	taken, err := m.idTaken(ctx, cc, id)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("%w: Objekt-ID %s ist im Buchungskreis %s bereits vergeben", sdk.ErrAlreadyExists, id, cc)
	}
	return nil
}

// nextID: <Kürzel><nnn> mit der kleinsten freien Nummer.
func (m *Module) nextID(ctx context.Context, cc, prefix string) (string, error) {
	for n := 1; n < 1000; n++ {
		id := fmt.Sprintf("%s%03d", prefix, n)
		taken, err := m.idTaken(ctx, cc, id)
		if err != nil {
			return "", err
		}
		if !taken {
			return id, nil
		}
	}
	return "", crud.Invalid("keine freie Objekt-ID für %s", prefix)
}

// assetRow: Art und Gültigkeit eines Objekts.
type assetRow struct{ Type, ValidFrom, ValidTo string }

func (m *Module) assetOf(ctx context.Context, cc, id string) (*assetRow, error) {
	res, err := m.db.Query(ctx, "SELECT asset_type, valid_from, valid_to FROM asset__asset WHERE company_code = ? AND asset_id = ?", cc, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Objekt %s gibt es im Buchungskreis %s nicht", id, cc)
	}
	from, _ := crud.ParseDate(res.Rows[0][1])
	to, _ := crud.ParseDate(res.Rows[0][2])
	return &assetRow{Type: crud.Str(res.Rows[0][0]), ValidFrom: from, ValidTo: to}, nil
}

// --- Bestandteile (Zeitscheibe) -----------------------------------------------------

// part: Ein Objekt ist zu einem Zeitpunkt Bestandteil höchstens eines anderen –
// z. B. die Winterreifen im Winter am Auto, im Sommer eingelagert (ohne Eintrag).
func (m *Module) part() *crud.Entity {
	return &crud.Entity{
		Object: "AssetPart", Title: "Bestandteile", Icon: "icon-layers", Table: "asset__part", Section: "Bestand",
		Keys: []string{"company_code", "asset_id", "part_id", "valid_from"}, TimeSlice: true,
		Order: "company_code, asset_id, valid_from, part_id", Filters: []string{"company_code", "asset_id", "part_id"},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"asset_id", "part_id", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "asset_id", Label: "Objekt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: assetLookup},
			crud.Field{Key: "part_id", Label: "Bestandteil", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: assetLookup},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText, Listable: true},
		),
		Access: access(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "Asset", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkPart,
	}
}

func (m *Module) checkPart(ctx context.Context, rec, _ crud.Record) error {
	cc := crud.Str(rec["company_code"])
	parent, part := strings.TrimSpace(crud.Str(rec["asset_id"])), strings.TrimSpace(crud.Str(rec["part_id"]))
	rec["asset_id"], rec["part_id"] = parent, part
	from, to := crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"])
	if strings.EqualFold(parent, part) {
		return crud.Invalid("Ein Objekt kann nicht Bestandteil von sich selbst sein")
	}
	p, err := m.assetOf(ctx, cc, parent)
	if err != nil {
		return err
	}
	c, err := m.assetOf(ctx, cc, part)
	if err != nil {
		return err
	}
	res, err := m.db.Query(ctx, "SELECT name, part_types FROM asset__asset_type WHERE company_code = ? AND code = ?", cc, p.Type)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 || !slices.Contains(splitCodes(crud.Str(res.Rows[0][1])), c.Type) {
		name := p.Type
		if len(res.Rows) > 0 {
			name = crud.Str(res.Rows[0][0])
		}
		return crud.Invalid("Objektart %s erlaubt keine Bestandteile der Art %s (Objekte → Einstellungen → Objektarten)", name, c.Type)
	}
	// Höchstens ein übergeordnetes Objekt je Zeitpunkt (ohne den Datensatz selbst).
	res, err = m.db.Query(ctx, `SELECT asset_id, valid_from, valid_to FROM asset__part
		WHERE company_code = ? AND part_id = ? AND valid_from <= ? AND valid_to >= ? AND NOT (asset_id = ? AND valid_from = ?)`,
		cc, part, to, from, parent, from)
	if err != nil {
		return err
	}
	if len(res.Rows) > 0 {
		f, _ := crud.ParseDate(res.Rows[0][1])
		t, _ := crud.ParseDate(res.Rows[0][2])
		return crud.Invalid("%s ist %s–%s bereits Bestandteil von %s – erst dort beenden", part, f, t, crud.Str(res.Rows[0][0]))
	}
	// Kein Kreis: Das übergeordnete Objekt darf im Zeitraum nicht selbst
	// (mittelbar) Bestandteil des neuen Bestandteils sein.
	seen := map[string]bool{parent: true}
	for cur := parent; ; {
		res, err := m.db.Query(ctx, `SELECT asset_id FROM asset__part WHERE company_code = ? AND part_id = ? AND valid_from <= ? AND valid_to >= ?`,
			cc, cur, to, from)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		cur = crud.Str(res.Rows[0][0])
		if strings.EqualFold(cur, part) {
			return crud.Invalid("%s ist im Zeitraum selbst Bestandteil von %s – kein Kreis", parent, part)
		}
		if seen[cur] {
			return nil
		}
		seen[cur] = true
	}
}

// --- Personen in Rollen (Zeitscheibe) -----------------------------------------------

func (m *Module) person() *crud.Entity {
	return &crud.Entity{
		Object: "AssetPerson", Title: "Personen am Objekt", Icon: "icon-users", Table: "asset__person", Section: "Bestand",
		Keys: []string{"company_code", "asset_id", "role_code", "partner_id", "valid_from"}, TimeSlice: true,
		Order:   "company_code, asset_id, role_code, valid_from, partner_id",
		Filters: []string{"company_code", "asset_id", "role_code", "partner_id"},
		Events:  true, CompanyCodeField: "company_code",
		EventFields: []string{"asset_id", "role_code", "partner_id", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "asset_id", Label: "Objekt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: assetLookup},
			crud.Field{Key: "role_code", Label: "Rolle", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("AssetRole")},
			crud.Field{Key: "partner_id", Label: "Person (Geschäftspartner)", Short: "Person", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"name1", "name2"}}},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText},
		),
		Access: access(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "AssetPerson", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkPerson,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			if name := m.partnerName(ctx, crud.Str(rec["partner_id"])); name != "" {
				setLabel(rec, "partner_id", name)
			}
			return nil
		},
	}
}

func (m *Module) checkPerson(ctx context.Context, rec, _ crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), strings.TrimSpace(crud.Str(rec["asset_id"]))
	role, partner := trimUpper(rec["role_code"]), strings.TrimSpace(crud.Str(rec["partner_id"]))
	rec["asset_id"], rec["role_code"], rec["partner_id"] = id, role, partner
	from, to := crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"])
	if _, err := m.assetOf(ctx, cc, id); err != nil {
		return err
	}
	res, err := m.db.Query(ctx, "SELECT name, is_exclusive FROM asset__asset_role WHERE company_code = ? AND code = ? AND is_active = ?", cc, role, true)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Rolle %s gibt es im Buchungskreis %s nicht (oder inaktiv) – Objekte → Einstellungen → Rollen am Objekt", role, cc)
	}
	roleName, exclusive := crud.Str(res.Rows[0][0]), crud.AsBool(res.Rows[0][1])
	if _, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": partner}); err != nil {
		return crud.Invalid("Person %s: %v", partner, err)
	}
	if !exclusive {
		return nil
	}
	res, err = m.db.Query(ctx, `SELECT partner_id, valid_from, valid_to FROM asset__person
		WHERE company_code = ? AND asset_id = ? AND role_code = ? AND partner_id <> ? AND valid_from <= ? AND valid_to >= ?`,
		cc, id, role, partner, to, from)
	if err != nil {
		return err
	}
	if len(res.Rows) > 0 {
		f, _ := crud.ParseDate(res.Rows[0][1])
		t, _ := crud.ParseDate(res.Rows[0][2])
		return crud.Invalid("%s an %s ist %s–%s bereits %s – erst dort beenden", roleName, id, f, t, m.partnerLabel(ctx, crud.Str(res.Rows[0][0])))
	}
	return nil
}

// partnerName: Name 1 und 2 des Partners; leer, wenn das Partnermodul fehlt
// oder der Benutzer den Partner nicht lesen darf.
func (m *Module) partnerName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	resp, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return ""
	}
	var bp struct {
		Name1 string `json:"name1"`
		Name2 string `json:"name2"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return ""
	}
	return strings.TrimSpace(bp.Name1 + " " + bp.Name2)
}

// partnerLabel: Nummer und Name, z. B. „1000005 Abraham“.
func (m *Module) partnerLabel(ctx context.Context, id string) string {
	return strings.TrimSpace(id + " " + m.partnerName(ctx, id))
}
