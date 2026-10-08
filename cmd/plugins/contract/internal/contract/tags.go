package contract

import (
	"context"
	"errors"
	"fmt"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Merkmale der Mieter (Tag-Plugin): Die Einrichtung des Buchungskreises legt
// fehlende Tags, das Tag Set MIETER und seine Zuordnung zu Geschäftspartnern
// der Art Person an. Bestehende Definitionen bleiben unverändert – Namen,
// Pflicht und weitere Tags pflegt die Tag-Verwaltung.

const tenantTagSet = "MIETER"

var tenantTags = []struct{ code, name, dataType string }{
	{"STEUER_ID", "Steuer-Identifikationsnummer", "STRING"},
	{"GEBURTSDATUM", "Geburtsdatum", "DATE"},
	{"AUSWEIS_NR", "Personalausweis-Nummer", "STRING"},
	{"AUSWEIS_DATUM", "Personalausweis ausgestellt am", "DATE"},
	{"AUSWEIS_BEHOERDE", "Personalausweis ausstellende Behörde", "STRING"},
	{"BRIEFANREDE", "Briefanrede", "STRING"},
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
		if err := ensure("TagType", t.code, map[string]any{"code": t.code, "name": t.name, "data_type": t.dataType, "value_mode": "FREE"}); err != nil {
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
		"tag_set_code": tenantTagSet, "condition_field": "type", "condition_values": "PERSON", "valid_from": from})
	return n, err
}
