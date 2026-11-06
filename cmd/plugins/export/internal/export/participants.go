package export

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/cm1"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Teilnehmer, Rollen und Freigaben (Konzept Ablage_Dateiformat_Konzept.txt 7.1–7.3)
//
//   - Rolle (Katalog): z. B. STB Steuerbüro, MIETER Mieter; PIN Pflicht für die PWA.
//   - Teilnehmer: wer Daten in der PWA liest (Mieter, Steuerbüro …), mit Rollen und Bezug
//     (Geschäftspartner); Einrichten erzeugt den Benutzerschlüssel und den Einrichtungslink.
//   - Freigabe je Sicht: Rolle + Bezugsregel (ALL: alle Datasets der Sicht, PARTNER: nur
//     Datasets des eigenen Geschäftspartners); ohne eigene Freigaben gilt der Vorschlag der
//     Sichtart.
//   - Zugriff: wer welches Dataset sieht (seit/bis) – daraus entsteht die Startdatei.

const (
	roleObject        = "ExportRole"
	contentTypeObject = "ExportContentType"
	participantObject = "ExportParticipant"
	partRoleObject    = "ExportParticipantRole"
	grantObject       = "ExportGrant"
	accessObject      = "ExportAccess"
	storeFileObject   = "ExportStoreFile"

	tnPrefix = "TN" // TN-JJJJ-NNNN

	tnDraft  = "DRAFT"
	tnActive = "ACTIVE"
	tnLocked = "LOCKED"

	ruleAll     = "ALL"
	rulePartner = "PARTNER"

	defaultModule = "explorer"
)

var participantStatus = []metamodel.Option{
	{Value: tnDraft, Label: "Entwurf (noch nicht eingerichtet)", Tone: metamodel.ToneInfo},
	{Value: tnActive, Label: "aktiv (eingerichtet)", Tone: metamodel.ToneSuccess},
	{Value: tnLocked, Label: "gesperrt", Tone: metamodel.ToneMuted},
}

var grantRules = []metamodel.Option{
	{Value: ruleAll, Label: "alle Datasets der Sicht"},
	{Value: rulePartner, Label: "nur Datasets des eigenen Geschäftspartners"},
}

var packingOptions = []metamodel.Option{
	{Value: cm1.PackNone, Label: "nicht packen (PDF, Bilder)"},
	{Value: cm1.PackGzip, Label: "packen (gzip; Text, JSON, CSV)"},
}

var lookupParticipant = &metamodel.Lookup{Object: participantObject, ValueField: "participant_id", LabelFields: []string{"name"},
	Filters: map[string]string{"company_code": "company_code"}}

var lookupStoreTarget = &metamodel.Lookup{Object: targetObject, ValueField: "code", LabelFields: []string{"name"},
	Filters: map[string]string{"company_code": "company_code", "kind": "=" + targetStore}}

// role: Katalog Rollen.
func (m *Module) role() *crud.Entity {
	return &crud.Entity{
		Object: roleObject, Customizing: true, Title: "Rollen der Teilnehmer", Icon: "icon-users", Table: "export__role", Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "pin_required", Label: "PIN in der App Pflicht", Short: "PIN", Type: tBool, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "sort_order", Label: "Reihenfolge in der Liste", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: catalogAccess(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, roleObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error { return codeCheck(rec, old) },
	}
}

// contentType: Katalog Content-Types (Packen, Endung).
func (m *Module) contentType() *crud.Entity {
	return &crud.Entity{
		Object: contentTypeObject, Customizing: true, Title: "Content-Types der Ablage", Icon: "icon-file", Table: "export__content_type", Section: "Einstellungen",
		Keys: []string{"company_code", "content_type"}, Order: "company_code, sort_order, content_type", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"content_type", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "content_type", Label: "Content-Type (z. B. application/pdf, image/* für alle Bilder, *+json für alle JSON-Arten)", Short: "Content-Type", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "packing", Label: "Packen", Type: tSel, Required: true, Listable: true, Options: packingOptions},
			{Key: "extension", Label: "Endung (z. B. .pdf)", Short: "Endung", Type: tText, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "sort_order", Label: "Reihenfolge in der Liste", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: catalogAccess(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, contentTypeObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			ct := strings.ToLower(strings.TrimSpace(crud.Str(rec["content_type"])))
			if old == nil {
				if !strings.Contains(ct, "/") && !strings.HasPrefix(ct, "*+") {
					return crud.Invalid("Content-Type %q: Art/Unterart erwartet (z. B. application/pdf, image/*, *+json)", ct)
				}
				rec["content_type"] = ct
			}
			rec["packing"] = strings.ToLower(strings.TrimSpace(crud.Str(rec["packing"])))
			if !validOption(packingOptions, crud.Str(rec["packing"])) {
				return crud.Invalid("Packen %q gibt es nicht (none, gzip)", crud.Str(rec["packing"]))
			}
			rec["extension"] = nilIfEmpty(strings.ToLower(strings.TrimSpace(crud.Str(rec["extension"]))))
			return nil
		},
	}
}

// packingFor: Packart nach dem Katalog (genau, dann art/*, dann *+suffix), sonst none.
func (m *Module) packingFor(ctx context.Context, cc, ct string) string {
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	cands := []string{ct}
	if a, _, ok := strings.Cut(ct, "/"); ok {
		cands = append(cands, a+"/*")
	}
	if i := strings.LastIndex(ct, "+"); i > 0 {
		cands = append(cands, "*"+ct[i:])
	}
	for _, c := range cands {
		if r, err := m.one(ctx, "SELECT packing FROM export__content_type WHERE company_code = ? AND content_type = ? AND is_active = ?", cc, c, true); err == nil && r != nil {
			return crud.Str(r[0])
		}
	}
	return cm1.PackNone
}

// participant: Teilnehmer mit Rollen; Einrichten, Sperren.
func (m *Module) participant() *crud.Entity {
	return &crud.Entity{
		Object: participantObject, Title: "Teilnehmer (App)", Icon: "icon-users", Table: "export__participant", Section: "Sichten",
		Keys: []string{"company_code", "participant_id"}, Order: "company_code, participant_id", TitleField: "name",
		Filters: []string{"company_code", "status", "partner_id", "target"}, Search: []string{"participant_id", "name", "email"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "participant_id", Label: "Teilnehmer", Short: "Nr.", Type: tText, Listable: true, ReadOnly: true},
			{Key: "name", Label: "Name (erscheint in der App)", Short: "Name", Type: tText, Required: true, Listable: true, Wide: true},
			{Key: "partner_id", Label: "Geschäftspartner (Bezug, z. B. der Mieter)", Short: "Partner", Type: tText, Listable: true, Lookup: lookupPartner},
			{Key: "user_id", Label: "Benutzer der Anmeldung (optional)", Short: "Benutzer", Type: tText, Lookup: crud.LookupUser},
			{Key: "email", Label: "E-Mail", Type: tText},
			{Key: "target", Label: "Ablage (Ziel der Art Objektspeicher)", Short: "Ablage", Type: tText, Required: true, Listable: true, Lookup: lookupStoreTarget},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: participantStatus},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "setup_at", Label: "Eingerichtet am", Type: tText, Listable: true, ReadOnly: true, Group: "App"},
			{Key: "setup_by", Label: "Eingerichtet von", Type: tText, ReadOnly: true, Group: "App", Lookup: crud.LookupUser},
			{Key: "key_no", Label: "Fassung des Benutzerschlüssels", Type: tNum, ReadOnly: true, Group: "App"},
			{Key: "user_key", Label: "Benutzerschlüssel", Type: tText, ReadOnly: true, Group: "App"},
			{Key: "start_version", Label: "Fassung der Startdatei", Type: tNum, ReadOnly: true, Group: "App"},
			{Key: "start_at", Label: "Startdatei geschrieben am", Type: tText, ReadOnly: true, Group: "App"},
			{Key: "locked_at", Label: "Gesperrt am", Type: tText, ReadOnly: true, Group: "App"},
			{Key: "created_at", Label: "Angelegt am", Type: tText, ReadOnly: true},
			{Key: "created_by", Label: "Angelegt von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "log", Label: "Protokoll", Type: tArea, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "rollen", Title: "Rollen", Relation: &metamodel.Relation{Object: partRoleObject, ForeignKey: "participant_id",
				Match: match("participant_id", "participant_id"), Columns: []string{"role_no", "role", "partner_id", "valid_from", "valid_to"}}},
			{Key: "sieht", Title: "Sieht (Datasets)", Relation: &metamodel.Relation{Object: accessObject, ForeignKey: "participant_id",
				Match: match("participant_id", "participant_id"), Columns: []string{"dataset_id", "role", "since", "until"}}},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "setup", Label: "Einrichten …", Icon: "icon-key", Record: true,
				Confirm: "Neuen Einrichtungslink erzeugen? Ein bisheriges Gerät dieses Teilnehmers sieht danach nichts mehr."}, Handle: m.participantSetupAction},
			{ActionConfig: metamodel.ActionConfig{Name: "lock", Label: "Sperren", Icon: "icon-lock", Record: true,
				Confirm: "Teilnehmer sperren? Die App löscht beim nächsten Öffnen alle Daten."}, Handle: m.participantLockAction},
		},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			cc := strings.TrimSpace(crud.Str(rec["company_code"]))
			if cc == "" {
				return nil
			}
			id, err := m.nextNumber(ctx, "export__participant", "participant_id", cc, tnPrefix, m.today()[:4])
			if err != nil {
				return err
			}
			rec["participant_id"], rec["status"], rec["created_at"], rec["created_by"] = id, tnDraft, m.stamp(), nilIfEmpty(userOf(ctx))
			return nil
		},
		Access: &crud.Access{Object: participantObject, Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, participantObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			rec["name"] = strings.TrimSpace(crud.Str(rec["name"]))
			for _, k := range []string{"partner_id", "user_id", "email", "note"} {
				rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
			}
			rec["target"] = trimUpper(rec["target"])
			if _, err := m.storeTarget(ctx, crud.Str(rec["company_code"]), crud.Str(rec["target"])); err != nil {
				return err
			}
			if old != nil && crud.Str(old["status"]) != tnDraft && crud.Str(old["target"]) != crud.Str(rec["target"]) {
				return crud.Invalid("Die Ablage eines eingerichteten Teilnehmers ist fest – für eine andere Ablage einen neuen Teilnehmer anlegen")
			}
			return nil
		},
	}
}

// participantRole: Rolle eines Teilnehmers mit Bezug und Gültigkeit.
func (m *Module) participantRole() *crud.Entity {
	return &crud.Entity{
		Object: partRoleObject, Title: "Rollen der Teilnehmer", Icon: "icon-users", Table: "export__participant_role", Section: "Sichten",
		Keys: []string{"company_code", "participant_id", "role_no"}, Order: "company_code, participant_id, role_no",
		Filters: []string{"company_code", "participant_id", "role"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "participant_id", Label: "Teilnehmer", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupParticipant},
			{Key: "role_no", Label: "Nr.", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "role", Label: "Rolle", Type: tText, Required: true, Listable: true, Lookup: catalogLookup(roleObject)},
			{Key: "partner_id", Label: "Bezug: Geschäftspartner (leer = der des Teilnehmers)", Short: "Bezug", Type: tText, Listable: true, Lookup: lookupPartner},
			{Key: "valid_from", Label: "Gültig ab", Short: "ab", Type: tDate, Listable: true},
			{Key: "valid_to", Label: "Gültig bis (leer = unbegrenzt)", Short: "bis", Type: tDate, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText},
		},
		Access: &crud.Access{Object: participantObject, Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, participantObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			rec["role"] = trimUpper(rec["role"])
			rec["partner_id"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["partner_id"])))
			if toInt(rec["role_no"]) <= 0 {
				return crud.Invalid("Nr. der Rolle: 1 oder größer")
			}
			for _, k := range []string{"valid_from", "valid_to"} {
				d, err := dateOrNil(rec[k])
				if err != nil {
					return err
				}
				rec[k] = d
			}
			if f, t := crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"]); f != "" && t != "" && t < f {
				return crud.Invalid("Gültig bis liegt vor gültig ab")
			}
			r, err := m.one(ctx, "SELECT 1 FROM export__role WHERE company_code = ? AND code = ? AND is_active = ?", crud.Str(rec["company_code"]), rec["role"], true)
			if err != nil {
				return err
			}
			if r == nil {
				return crud.Invalid("Rolle %s gibt es nicht (Katalog Rollen der Teilnehmer)", crud.Str(rec["role"]))
			}
			return nil
		},
	}
}

// grant: Freigabe einer Sicht für eine Rolle.
func (m *Module) grant() *crud.Entity {
	return &crud.Entity{
		Object: grantObject, Title: "Freigaben der Sichten", Icon: "icon-share", Table: "export__grant", Section: "Sichten",
		Keys: []string{"company_code", "view_id", "grant_no"}, Order: "company_code, view_id, grant_no", StatusField: "is_active",
		Filters: []string{"company_code", "view_id", "role"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "view_id", Label: "Sicht", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogViewLookup},
			{Key: "grant_no", Label: "Nr.", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "role", Label: "Rolle", Type: tText, Required: true, Listable: true, Lookup: catalogLookup(roleObject)},
			{Key: "rule", Label: "Welche Datasets", Short: "Regel", Type: tSel, Required: true, Listable: true, Options: grantRules},
			{Key: "module", Label: "Modul der App (leer = explorer, der File Explorer)", Short: "Modul", Type: tText, Listable: true, Lookup: lookupModule},
			{Key: "valid_to", Label: "Freigabe bis (leer = solange das Dataset besteht)", Short: "bis", Type: tDate, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Object: viewObject, Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, viewObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			rec["role"], rec["rule"] = trimUpper(rec["role"]), trimUpper(rec["rule"])
			rec["module"] = nilIfEmpty(strings.ToLower(strings.TrimSpace(crud.Str(rec["module"]))))
			if toInt(rec["grant_no"]) <= 0 {
				return crud.Invalid("Nr. der Freigabe: 1 oder größer")
			}
			if !validOption(grantRules, crud.Str(rec["rule"])) {
				return crud.Invalid("Regel %q gibt es nicht (ALL, PARTNER)", crud.Str(rec["rule"]))
			}
			d, err := dateOrNil(rec["valid_to"])
			if err != nil {
				return err
			}
			rec["valid_to"] = d
			r, err := m.one(ctx, "SELECT 1 FROM export__role WHERE company_code = ? AND code = ? AND is_active = ?", crud.Str(rec["company_code"]), rec["role"], true)
			if err != nil {
				return err
			}
			if r == nil {
				return crud.Invalid("Rolle %s gibt es nicht (Katalog Rollen der Teilnehmer)", crud.Str(rec["role"]))
			}
			if mod := crud.Str(rec["module"]); mod != "" {
				if r, err := m.one(ctx, "SELECT 1 FROM export__module WHERE company_code = ? AND module_id = ?", crud.Str(rec["company_code"]), mod); err != nil {
					return err
				} else if r == nil {
					return crud.Invalid("Modul %s gibt es nicht (Katalog Module der App)", mod)
				}
			}
			return nil
		},
	}
}

// access: wer welches Dataset sieht (entsteht beim Veröffentlichen).
func (m *Module) access() *crud.Entity {
	return &crud.Entity{
		Object: accessObject, Title: "Zugriffe der Teilnehmer", Icon: "icon-eye", Table: "export__access", Section: "Sichten", ReadOnly: true,
		Keys: []string{"company_code", "participant_id", "dataset_id"}, Order: "company_code, participant_id, dataset_id",
		Filters: []string{"company_code", "participant_id", "dataset_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "participant_id", Label: "Teilnehmer", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupParticipant},
			{Key: "dataset_id", Label: "Dataset", Type: tText, Listable: true, ReadOnly: true,
				Lookup: &metamodel.Lookup{Object: datasetObject, ValueField: "dataset_id", LabelFields: []string{"name"}, Filters: map[string]string{"company_code": "company_code"}}},
			{Key: "role", Label: "über Rolle", Short: "Rolle", Type: tText, Listable: true, ReadOnly: true, Lookup: catalogLookup(roleObject)},
			{Key: "since", Label: "Sieht seit", Short: "seit", Type: tText, Listable: true, ReadOnly: true},
			{Key: "until", Label: "Bis (Berechtigung entzogen)", Short: "bis", Type: tText, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Object: participantObject, Records: true, CompanyCode: "company_code"},
	}
}

// nextNumber: PREFIX-JJJJ-NNNN fortlaufend je Buchungskreis und Jahr.
func (m *Module) nextNumber(ctx context.Context, table, col, cc, prefix, year string) (string, error) {
	stem := prefix + "-" + year + "-"
	r, err := m.one(ctx, "SELECT MAX("+col+") FROM "+table+" WHERE company_code = ? AND "+col+" LIKE ?", cc, stem+"%")
	if err != nil {
		return "", err
	}
	n := 0
	if r != nil && crud.Str(r[0]) != "" {
		fmt.Sscanf(strings.TrimPrefix(crud.Str(r[0]), stem), "%d", &n)
	}
	return fmt.Sprintf("%s%04d", stem, n+1), nil
}

// participantSetupAction: ExportParticipant.setup – Benutzerschlüssel (neue Fassung) und
// Startdatei; der Einrichtungslink mit dem privaten Schlüssel erscheint einmal.
func (m *Module) participantSetupAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitKey(in.ID, "Teilnehmer")
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, participantObject, "setup", cc); err != nil {
		return sdk.Response{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.one(ctx, `SELECT name, COALESCE(target, ''), status, COALESCE(uid, ''), COALESCE(key_no, 0), COALESCE(start_id, '')
		FROM export__participant WHERE company_code = ? AND participant_id = ?`, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if p == nil {
		return sdk.Response{}, fmt.Errorf("%w: Teilnehmer %s", sdk.ErrNotFound, in.ID)
	}
	name, tgtCode, status, uid, keyNo, oldStart := crud.Str(p[0]), crud.Str(p[1]), crud.Str(p[2]), crud.Str(p[3]), toInt(p[4]), crud.Str(p[5])
	tgt, err := m.storeTarget(ctx, cc, tgtCode)
	if err != nil {
		return sdk.Response{}, err
	}
	if tgt.publicURL == "" || tgt.pwaURL == "" {
		return sdk.Response{}, crud.Invalid("Ablage %s: Adresse der Ablage und Adresse der App fehlen (Ziel bearbeiten)", tgt.code)
	}
	sk, err := m.serverKeys(ctx)
	if err != nil {
		return sdk.Response{}, err
	}
	// altes Gerät: Startdatei leeren (mit dem alten Schlüssel, den es noch hat)
	if oldStart != "" && status == tnActive {
		if err := m.writeEmptyStart(ctx, cc, id, tgt, sk); err != nil {
			return sdk.Response{}, err
		}
	}
	if uid == "" {
		uid = cm1.NewID()
	}
	keyNo++
	secret, public, err := cm1.NewAgeKey()
	if err != nil {
		return sdk.Response{}, err
	}
	keyName := fmt.Sprintf("user:%s:%d", uid, keyNo)
	startID := cm1.NewID()
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if err := m.insertKey(ctx, keyName, keyUser, keyNo, cc, "", id, public, ""); err != nil {
			return err
		}
		if oldStart != "" {
			if _, err := m.db.Exec(ctx, "UPDATE export__store_file SET unused_since = ? WHERE company_code = ? AND target = ? AND path = ? AND unused_since IS NULL",
				m.stamp(), cc, tgt.code, "i/"+oldStart); err != nil {
				return err
			}
		}
		_, err := m.db.Exec(ctx, `UPDATE export__participant SET status = ?, uid = ?, key_no = ?, user_key = ?, user_public = ?, start_id = ?, start_version = 0,
			start_hash = NULL, setup_at = ?, setup_by = ?, locked_at = NULL, log = CASE WHEN COALESCE(log, '') = '' THEN '' ELSE log || ? END || ?
			WHERE company_code = ? AND participant_id = ?`, tnActive, uid, keyNo, keyName, public, startID, m.stamp(), nilIfEmpty(userOf(ctx)), "\n",
			fmt.Sprintf("%s eingerichtet von %s (Schlüssel %s)", m.stamp(), firstNonEmpty(userOf(ctx), "System"), keyName), cc, id)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.publish(ctx, cc, tgt.code); err != nil {
		return sdk.Response{}, err
	}
	sig := sk.sigs[0]
	link := cm1.SetupLink{Ablage: tgt.publicURL, Start: startID, Name: name, Key: cm1.LinkKey{Name: keyName, Secret: secret},
		Sig: cm1.LinkSigner{Key: sig.name, Public: sig.public}}.URL(tgt.pwaURL)
	return sdk.Response{Payload: map[string]any{
		"message": fmt.Sprintf("%s eingerichtet. Den QR-Code mit dem Handy scannen bzw. den Link weitergeben – er enthält den privaten Schlüssel und wird nur jetzt angezeigt.", name),
		"qr":      []map[string]any{{"text": link, "caption": "Einrichtung für " + name, "level": "L"}},
		"copy":    []map[string]any{{"label": "Einrichtungslink", "value": link}},
	}}, nil
}

// participantLockAction: ExportParticipant.lock – Startdatei leeren, Zugriffe entziehen.
func (m *Module) participantLockAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitKey(in.ID, "Teilnehmer")
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, participantObject, "lock", cc); err != nil {
		return sdk.Response{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.one(ctx, "SELECT status, COALESCE(target, '') FROM export__participant WHERE company_code = ? AND participant_id = ?", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if p == nil {
		return sdk.Response{}, fmt.Errorf("%w: Teilnehmer %s", sdk.ErrNotFound, in.ID)
	}
	if crud.Str(p[0]) == tnLocked {
		return sdk.Response{}, crud.Invalid("Teilnehmer %s ist schon gesperrt", id)
	}
	if _, err := m.db.Exec(ctx, `UPDATE export__participant SET status = ?, locked_at = ?, log = CASE WHEN COALESCE(log, '') = '' THEN '' ELSE log || ? END || ?
		WHERE company_code = ? AND participant_id = ?`, tnLocked, m.stamp(), "\n", fmt.Sprintf("%s gesperrt von %s", m.stamp(), firstNonEmpty(userOf(ctx), "System")), cc, id); err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Teilnehmer %s gesperrt", id)
	if crud.Str(p[0]) == tnActive {
		out, err := m.publish(ctx, cc, crud.Str(p[1]))
		if err != nil {
			return sdk.Response{}, err
		}
		msg += " – " + out
	}
	return sdk.Response{Payload: map[string]any{"message": msg}}, nil
}
