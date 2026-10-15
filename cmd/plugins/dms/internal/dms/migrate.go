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
//   - 0.3.5: Archive sperren – Verzeichnisse nur lesbar (abgelegte Dokumente nicht löschbar).
//   - 0.10.0: KI-Schritte „Geschäftspartner anlegen“ (bind=partner) bekommen Vorschau,
//     Datei auf Nachfrage und Hinweise aus Dokument/Kontoauszug – nur fehlende Parameter;
//     der Kontext nur, wenn er noch der mitgelieferte ist.
//   - 0.11.0: Erkennungsschritte (IDENTIFY) bekommen ihre Punkte als Parameter (nur fehlende).
//   - 0.11.0: Partnerauswahl (Quelle BusinessPartner.list, Ziel partner_id) bekommt
//     category_filter (Rechnung, Beleg: nur Kreditoren), wenn der Parameter fehlt.
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
	if err := m.migratePartnerSteps(ctx); err != nil {
		return err
	}
	for _, p := range identifyParams() {
		if _, err := m.db.Exec(ctx, `INSERT INTO dms__step_param (workflow, step, param, locale, value, is_active)
			SELECT s.workflow, s.step, ?, '', ?, ? FROM dms__step s WHERE s.kind = ?
			AND NOT EXISTS (SELECT 1 FROM dms__step_param p WHERE p.workflow = s.workflow AND p.step = s.step AND p.param = ?)`,
			p[0], p[2], true, kIdentify, p[0]); err != nil {
			return err
		}
	}
	if _, err := m.db.Exec(ctx, `INSERT INTO dms__step_param (workflow, step, param, locale, value, is_active)
		SELECT s.workflow, s.step, 'category_filter', '', ?, ? FROM dms__step s
		WHERE s.kind = ? AND s.target = 'partner_id'
		AND EXISTS (SELECT 1 FROM dms__step_param p WHERE p.workflow = s.workflow AND p.step = s.step AND p.param = 'source' AND p.value = 'BusinessPartner.list')
		AND NOT EXISTS (SELECT 1 FROM dms__step_param p WHERE p.workflow = s.workflow AND p.step = s.step AND p.param = 'category_filter')`,
		partnerCategoryFilter, true, kSelect); err != nil {
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
	return nil
}

func (m *Module) tableExists(ctx context.Context, name string) bool {
	res, err := m.db.Query(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, name)
	return err == nil && len(res.Rows) > 0 && crud.Str(res.Rows[0][0]) != ""
}

// migratePartnerSteps: 0.10.0 – Parameter der Partner-Schritte ergänzen (wiederholbar).
func (m *Module) migratePartnerSteps(ctx context.Context) error {
	res, err := m.db.Query(ctx, `SELECT s.workflow, s.step FROM dms__step s JOIN dms__step_param p ON p.workflow = s.workflow AND p.step = s.step
		AND p.param = 'bind' AND p.value = 'partner' WHERE s.kind = ?`, kAIObject)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		wf, step := crud.Str(r[0]), crud.Str(r[1])
		for _, p := range partnerStepParams {
			if p[0] == "context" {
				if _, err := m.db.Exec(ctx, `UPDATE dms__step_param SET value = ? WHERE workflow = ? AND step = ? AND param = 'context' AND locale = '' AND value = ?`,
					p[2], wf, step, partnerContextOld); err != nil {
					return err
				}
				continue
			}
			if _, err := m.db.Exec(ctx, `INSERT INTO dms__step_param (workflow, step, param, locale, value, is_active)
				SELECT ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM dms__step_param WHERE workflow = ? AND step = ? AND param = ?)`,
				wf, step, p[0], p[1], p[2], true, wf, step, p[0]); err != nil {
				return err
			}
		}
	}
	return nil
}
