package dms

import (
	"context"
	"path"
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
//   - 0.3.5: Archive sperren – Verzeichnisse nur lesbar (abgelegte Dokumente nicht löschbar).
//   - 0.12.0: Workflows in Form 2 (wfmigrate.go): ERP und STANDARD neu aus der
//     Vorlage, andere Workflows aus ihren Parametern übernommen; wartende
//     Dokumente bleiben am gleichen Schritt und bekommen ihren Ablauf. Die
//     Erkennungsregel „Name laut Kontoauszug“ zählt für den Partner.
//   - 0.12.1: Ablagefindung über Kostenart und „umlagefähig“ (Rechnungspositionen);
//     ERP 160 „Ablage wählen“ fragt auch bei schon gesetzter Ablage.
//   - 0.12.3: Auswahl der Rechnungsvorlage mit Position der Kondition.
//   - 0.13.2: Eingangsverzeichnis beim Eingang (received_inbox) für vorhandene Dokumente.
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
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET doc_category = (SELECT category FROM dms__doc_type t WHERE t.code = dms__document.doc_type)
		WHERE (doc_category IS NULL OR doc_category = '') AND doc_type IS NOT NULL`); err != nil {
		return err
	}
	// 0.12.0: Workflows in Form 2, Erkennungsregel des Kontoauszugs, Ablauf der wartenden Dokumente.
	if err := m.migrateWorkflows(ctx); err != nil {
		return err
	}
	if _, err := m.db.Exec(ctx, `UPDATE dms__hint SET target = 'partner_id', points = 5 WHERE code = 'payee' AND (target IS NULL OR target = '')`); err != nil {
		return err
	}
	// 0.12.3: Rechnungsvorlage des Vertrags mit Position der Kondition (contract 0.29.0).
	if _, err := m.db.Exec(ctx, `UPDATE dms__step SET value_field = '{condition_type}|{object_id}|{position}'
		WHERE source_object = 'ContractCondition' AND value_field = '{condition_type}|{object_id}'`); err != nil {
		return err
	}
	// 0.12.1: Ablagefindung auch über Kostenart und „umlagefähig“ der Rechnungspositionen.
	if _, err := m.db.Exec(ctx, `UPDATE dms__category_object SET cost_category_field = 'cost_category', allocable_field = 'allocable'
		WHERE account_object = 'SupplierInvoiceItem' AND cost_category_field IS NULL AND allocable_field IS NULL`); err != nil {
		return err
	}
	// 0.20.0: Angebot und Auftrag tragen die gewählte Ablage.
	if _, err := m.db.Exec(ctx, `UPDATE dms__category_object SET file_account_field = 'file_account'
		WHERE object IN ('PurchaseQuote', 'PurchaseOrder') AND file_account_field IS NULL`); err != nil {
		return err
	}
	// 0.13.2: Eingangsverzeichnis beim Eingang – bei übergebenen Dokumenten das
	// aus dem Dateinamen (…_step_over_from_<Eingang>), sonst das jetzige.
	if err := m.migrateReceivedInbox(ctx); err != nil {
		return err
	}
	// 0.3.5: Archive sperren (Verzeichnisse 0555, Dateien 0444) – abgelegte Dokumente nicht löschbar.
	res, err := m.db.Query(ctx, `SELECT code, kind, root FROM dms__archive WHERE is_active = ?`, true)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		a, err := openArchive(crud.Str(r[1]), crud.Str(r[2]))
		if err != nil {
			continue
		}
		if s, ok := a.(interface{ Seal() error }); ok {
			if err := s.Seal(); err != nil {
				m.log.WarnContext(ctx, "Archiv nicht gesperrt", "archive", crud.Str(r[0]), "err", err.Error())
			}
		}
	}
	// 0.16.1: Verweise am Ziel für Bezüge, die nach dem Ablegen dazukamen.
	if err := m.attachMissingRefs(ctx); err != nil {
		return err
	}
	// 0.15.0: Suchtext der Bezüge für Dokumente, die noch keinen haben.
	if n, err := m.refreshSearchAll(ctx, "", true); err != nil {
		return err
	} else if n > 0 {
		m.log.InfoContext(ctx, "Suchtexte der Bezüge aufgebaut", "documents", n)
	}
	return nil
}

func (m *Module) migrateReceivedInbox(ctx context.Context) error {
	res, err := m.db.Query(ctx, `SELECT id, inbox, file_name FROM dms__document
		WHERE (received_inbox IS NULL OR received_inbox = '') AND inbox IS NOT NULL AND inbox <> ''`)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		from := crud.Str(r[1])
		name := crud.Str(r[2])
		if i := strings.LastIndex(name, "_step_over_from_"); i >= 0 {
			if f := strings.TrimSuffix(name[i+len("_step_over_from_"):], path.Ext(name)); f != "" {
				from = f
			}
		}
		if _, err := m.db.Exec(ctx, `UPDATE dms__document SET received_inbox = ? WHERE id = ?`, from, crud.Str(r[0])); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) tableExists(ctx context.Context, name string) bool {
	res, err := m.db.Query(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, name)
	return err == nil && len(res.Rows) > 0 && crud.Str(res.Rows[0][0]) != ""
}
