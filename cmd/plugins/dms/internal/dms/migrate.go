package dms

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

var _ module.Migrator = (*Module)(nil)

// Migrate stellt Daten älterer Versionen um (wiederholbar):
//
//   - 0.2.2: Dokumentarten ohne Kategorie bekommen die des Vorschlags;
//     kaufmännische Objekte je Dokumentart (dms__doc_type_object, 0.2.0/0.2.1)
//     gehen an die Kategorie ihrer Dokumentart (dms__category_object);
//     Dokumente ohne Kategorie bekommen die ihrer Dokumentart.
//   - 0.3.4: Dokumentarten ohne Kürzel bekommen das des Vorschlags (wenn frei).
func (m *Module) Migrate(ctx context.Context) error {
	if !m.tableExists(ctx, "dms__doc_category") {
		return nil // Schema noch nicht angelegt
	}
	for _, r := range seedDocTypes {
		if _, err := m.db.Exec(ctx, `UPDATE dms__doc_type SET category = ? WHERE code = ? AND (category IS NULL OR category = '')
			AND EXISTS (SELECT 1 FROM dms__doc_category WHERE code = ?)`, r["category"], r["code"], r["category"]); err != nil {
			return err
		}
	}
	// 0.3.4: Kürzel der Dokumentarten (wo frei).
	for _, r := range seedDocTypes {
		if _, err := m.db.Exec(ctx, `UPDATE dms__doc_type SET short = ? WHERE code = ? AND (short IS NULL OR short = '')
			AND NOT EXISTS (SELECT 1 FROM dms__doc_type x WHERE x.short = ?)`, r["short"], r["code"], r["short"]); err != nil {
			return err
		}
	}
	if m.tableExists(ctx, "dms__doc_type_object") {
		cols := "object, label, list_action, filters, partner_field, date_field, title_field, account_object, account_link, account_field, can_create, sort_order, is_active"
		sel := strings.ReplaceAll("o."+cols, ", ", ", o.")
		if _, err := m.db.Exec(ctx, `INSERT INTO dms__category_object (category, `+cols+`)
			SELECT t.category, `+sel+` FROM dms__doc_type_object o JOIN dms__doc_type t ON t.code = o.doc_type
			WHERE t.category IS NOT NULL AND t.category <> ''
			AND NOT EXISTS (SELECT 1 FROM dms__category_object c WHERE c.category = t.category AND c.object = o.object)
			GROUP BY t.category, o.object`); err != nil {
			return err
		}
	}
	_, err := m.db.Exec(ctx, `UPDATE dms__document SET doc_category = (SELECT category FROM dms__doc_type t WHERE t.code = dms__document.doc_type)
		WHERE (doc_category IS NULL OR doc_category = '') AND doc_type IS NOT NULL`)
	return err
}

func (m *Module) tableExists(ctx context.Context, name string) bool {
	res, err := m.db.Query(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, name)
	return err == nil && len(res.Rows) > 0 && crud.Str(res.Rows[0][0]) != ""
}
