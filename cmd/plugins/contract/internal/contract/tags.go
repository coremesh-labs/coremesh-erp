package contract

import (
	"context"
	"errors"
	"fmt"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Merkmale der Mieter (Tag-Plugin): Die Einrichtung des Buchungskreises legt
// fehlende Tags, das Tag Set MIETER und seine Zuordnung zu Geschäftspartnern
// an, die natürliche Personen sind und heute die Rolle Mieter haben.
// Steuer-ID, Geburtsdatum und Ausweisdaten sind geschützt (Recht
// TagType.readValue/changeValue), die Steuer-ID hat ein Prüfmuster.
// Bestehende Definitionen bleiben unverändert – Namen, Pflicht, Schutz und
// weitere Tags pflegt die Tag-Verwaltung.

const tenantTagSet = "MIETER"

var tenantTags = []struct {
	code, name, dataType, pattern, hint string
	protected                           bool
}{
	{"STEUER_ID", "Steuer-Identifikationsnummer", "STRING", `[1-9]\d{10}`, "11 Ziffern, ohne Leerzeichen", true},
	{"GEBURTSDATUM", "Geburtsdatum", "DATE", "", "", true},
	{"AUSWEIS_NR", "Personalausweis-Nummer", "STRING", `[0-9A-Z]{9}`, "9 Zeichen (Ziffern und Großbuchstaben)", true},
	{"AUSWEIS_DATUM", "Personalausweis ausgestellt am", "DATE", "", "", true},
	{"AUSWEIS_BEHOERDE", "Personalausweis ausstellende Behörde", "STRING", "", "", true},
	{"BRIEFANREDE", "Briefanrede", "STRING", "", "", false},
}

// setupTenantTags liefert die Zahl der angelegten Definitionen; ohne
// Tag-Plugin (nicht gestartet) nichts.
func (m *Module) setupTenantTags(ctx context.Context, cc string) (int, error) {
	n := 0
	ensure := func(object, id string, data map[string]any) error {
		_, err := m.services.Call(ctx, object, "get", map[string]any{"id": id})
		if err == nil {
			return nil
		}
		if !errors.Is(err, sdk.ErrNotFound) {
			return err
		}
		if _, err := m.services.Call(ctx, object, "create", map[string]any{"data": data}); err != nil {
			return fmt.Errorf("%s %s: %w", object, id, err)
		}
		n++
		return nil
	}
	const from = "1900-01-01"
	for _, t := range tenantTags {
		data := map[string]any{"code": t.code, "name": t.name, "data_type": t.dataType, "value_mode": "FREE", "protected": t.protected}
		if t.pattern != "" {
			data["pattern"], data["pattern_hint"] = t.pattern, t.hint
		}
		if err := ensure("TagType", t.code, data); err != nil {
			return n, err
		}
	}
	if err := ensure("TagSet", tenantTagSet, map[string]any{"code": tenantTagSet, "name": "Mieter (Person)", "valid_from": from}); err != nil {
		return n, err
	}
	for i, t := range tenantTags {
		if err := ensure("TagSetItem", tenantTagSet+"|"+t.code, map[string]any{"tag_set_code": tenantTagSet, "tag_type_code": t.code,
			"mandatory": false, "sort_order": (i + 1) * 10, "valid_from": from}); err != nil {
			return n, err
		}
	}
	err := ensure("TagSetAssignment", "BusinessPartner|"+cc+"|"+tenantTagSet, map[string]any{"entity_type": "BusinessPartner", "company_code": cc,
		"tag_set_code": tenantTagSet, "condition_field": "type", "condition_values": "PERSON",
		"condition_field_2": "roles", "condition_values_2": "TENANT", "valid_from": from})
	return n, err
}

// Merkmale der Versorgungsverträge (seit 0.25.0): Tag Set VERSORGUNG am Vertrag der
// Vertragsart VE. Die Sparte (Strom, Gas, Fernwärme, Wasser) steuert über Regeln
// „sichtbar wenn“, welche Tags erscheinen – ohne eigene Vertragsart je Sparte.
const supplyTagSet = "VERSORGUNG"

var supplyTags = []struct {
	code, name, dataType, mode, pattern, hint, showIf string
}{
	{"SPARTE", "Sparte", "STRING", "OPTIONS", "", "", ""},
	{"ZAEHLER_NR", "Zählernummer", "STRING", "FREE", "", "", ""},
	{"NETZBETREIBER", "Netzbetreiber", "STRING", "FREE", "", "", ""},
	{"VERBRAUCH_PROGNOSE", "Verbrauchsprognose je Jahr (kWh bzw. m³)", "INTEGER", "FREE", "", "", ""},
	{"KALK_SCHEMA", "Kalkulationsschema der Rechnungen (z. B. STROM)", "STRING", "FREE", "", "", ""},
	{"MALO_ID", "Marktlokations-ID (MaLo)", "STRING", "FREE", `\d{11}`, "11 Ziffern", "STROM"},
	{"MELO_ID", "Messlokations-ID (MeLo)", "STRING", "FREE", `DE[0-9A-Z]{31}`, "DE und 31 Zeichen (Ziffern, Großbuchstaben)", "STROM"},
	{"ANSCHLUSSLEISTUNG_KW", "Anschlussleistung (kW)", "INTEGER", "FREE", "", "", "FERNWAERME"},
	{"UEBERGABESTATION", "Übergabestation", "STRING", "FREE", "", "", "FERNWAERME"},
}

var supplySparten = [][2]string{{"STROM", "Strom"}, {"GAS", "Gas"}, {"FERNWAERME", "Fernwärme"}, {"WASSER", "Wasser / Abwasser"}}

// setupSupplyTags legt fehlende Tags, Auswahlwerte, das Tag Set VERSORGUNG mit Regeln
// und seine Zuordnung zu Verträgen der Art VE im Buchungskreis an.
func (m *Module) setupSupplyTags(ctx context.Context, cc string) (int, error) {
	n := 0
	ensure := func(object, id string, data map[string]any) error {
		_, err := m.services.Call(ctx, object, "get", map[string]any{"id": id})
		if err == nil {
			return nil
		}
		if !errors.Is(err, sdk.ErrNotFound) {
			return err
		}
		if _, err := m.services.Call(ctx, object, "create", map[string]any{"data": data}); err != nil {
			return fmt.Errorf("%s %s: %w", object, id, err)
		}
		n++
		return nil
	}
	const from = "1900-01-01"
	for _, t := range supplyTags {
		data := map[string]any{"code": t.code, "name": t.name, "data_type": t.dataType, "value_mode": t.mode, "protected": false}
		if t.pattern != "" {
			data["pattern"], data["pattern_hint"] = t.pattern, t.hint
		}
		if err := ensure("TagType", t.code, data); err != nil {
			return n, err
		}
	}
	for i, o := range supplySparten {
		if err := ensure("TagValueOption", "SPARTE|"+o[0], map[string]any{"tag_type_code": "SPARTE", "code": o[0], "label": o[1], "sort_order": (i + 1) * 10}); err != nil {
			return n, err
		}
	}
	if err := ensure("TagSet", supplyTagSet, map[string]any{"code": supplyTagSet, "name": "Versorgungsvertrag", "valid_from": from}); err != nil {
		return n, err
	}
	for i, t := range supplyTags {
		if err := ensure("TagSetItem", supplyTagSet+"|"+t.code, map[string]any{"tag_set_code": supplyTagSet, "tag_type_code": t.code,
			"mandatory": t.code == "SPARTE", "sort_order": (i + 1) * 10, "valid_from": from}); err != nil {
			return n, err
		}
	}
	// Regeln „sichtbar wenn Sparte = …“ (Surrogatschlüssel: vorhandene über die Liste prüfen)
	have := map[string]bool{}
	if resp, err := m.services.Call(ctx, "TagSetRule", "list", map[string]any{"query": map[string]any{"tag_set_code": supplyTagSet}}); err == nil {
		var l struct {
			Items []map[string]any `json:"items"`
		}
		_ = sdk.Decode(resp.Payload, &l)
		for _, r := range l.Items {
			have[fmt.Sprint(r["rule_type"], "|", r["source_tag"], "|", r["condition_value"], "|", r["target_tag"])] = true
		}
	}
	for _, t := range supplyTags {
		if t.showIf == "" || have["SHOW_IF|SPARTE|"+t.showIf+"|"+t.code] {
			continue
		}
		if _, err := m.services.Call(ctx, "TagSetRule", "create", map[string]any{"data": map[string]any{"tag_set_code": supplyTagSet,
			"rule_type": "SHOW_IF", "source_tag": "SPARTE", "condition_value": t.showIf, "target_tag": t.code, "valid_from": from}}); err != nil {
			return n, fmt.Errorf("TagSetRule %s: %w", t.code, err)
		}
		n++
	}
	err := ensure("TagSetAssignment", "Contract|"+cc+"|"+supplyTagSet, map[string]any{"entity_type": "Contract", "company_code": cc,
		"tag_set_code": supplyTagSet, "condition_field": "contract_type", "condition_values": "VE", "valid_from": from})
	return n, err
}
