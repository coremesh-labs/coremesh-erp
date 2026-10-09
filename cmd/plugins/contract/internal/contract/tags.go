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
