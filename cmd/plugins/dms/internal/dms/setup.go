package dms

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// setupCompanyCommand: Archiv (falls nötig), Einstellungen des Buchungskreises,
// alle Ablagen aktivieren und den Standard-Workflow anlegen (fehlende Einträge).
func (m *Module) setupCompanyCommand(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Plan      string `json:"plan"`
		Archive   string `json:"archive"`
		Root      string `json:"root"`
		ShortName string `json:"short_name"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := companyOf(req.Payload)
	archive, plan := trimUpper(in.Archive), trimUpper(in.Plan)
	var notes []string
	if _, err := m.set.Entity(archiveObject).Load(ctx, crud.Record{"code": archive}); err != nil {
		if strings.TrimSpace(in.Root) == "" {
			return sdk.Response{}, crud.Invalid("Archiv %s gibt es nicht – Wurzelverzeichnis (root) angeben, dann wird es angelegt", archive)
		}
		if _, err := m.set.Entity(archiveObject).Create(ctx, map[string]any{"data": map[string]any{"code": archive, "name": "Archiv " + archive,
			"kind": "FILE", "root": in.Root}}); err != nil {
			return sdk.Response{}, err
		}
		notes = append(notes, "Archiv "+archive+" angelegt")
	}
	companies := m.set.Entity(companyObject)
	if _, err := companies.Load(ctx, crud.Record{"company_code": cc}); err != nil {
		if _, err := companies.Create(ctx, map[string]any{"data": map[string]any{"company_code": cc, "plan": plan, "archive": archive,
			"short_name": in.ShortName}}); err != nil {
			return sdk.Response{}, err
		}
		notes = append(notes, "Buchungskreis "+cc+" eingerichtet")
	}
	n, err := m.activateAccounts(ctx, cc)
	if err != nil {
		return sdk.Response{}, err
	}
	notes = append(notes, fmt.Sprintf("%d Ablagen aktiviert", n))
	created, err := m.ensureStandardWorkflow(ctx)
	if err != nil {
		return sdk.Response{}, err
	}
	if created {
		notes = append(notes, "Workflow "+standardWorkflow+" angelegt")
	}
	return sdk.Response{Payload: map[string]any{"message": "DMS " + cc + ": " + strings.Join(notes, ", ")}}, nil
}

const standardWorkflow = "STANDARD"

// ensureStandardWorkflow legt den Standard-Workflow an, falls er fehlt:
// Text erkennen → Datum suchen → Dokumentart → Ablage → Partner → Datum →
// Titel → bestätigen → ablegen. Felder, die das Eingangsverzeichnis vorgibt,
// werden übersprungen.
func (m *Module) ensureStandardWorkflow(ctx context.Context) (bool, error) {
	if _, err := m.set.Entity(workflowObject).Load(ctx, crud.Record{"code": standardWorkflow}); err == nil {
		return false, nil
	}
	type st struct {
		step, name, kind, target string
		params                   [][3]string // param, locale, value
		options                  [][2]string // outcome, next
	}
	steps := []st{
		{"010", "Text erkennen", kOCR, "", nil, [][2]string{{oSuccess, "020"}, {oFailed, "030"}}},
		{"020", "Datum im Text suchen", kExtract, "doc_date", [][3]string{{"pattern", "", `(\d{1,2}\.\d{1,2}\.\d{4})`}},
			[][2]string{{oSuccess, "030"}, {oFailed, "030"}}},
		{"030", "Dokumentart wählen", kSelect, "doc_type", [][3]string{
			{"question", "de", "Welche Dokumentart ist es?"}, {"question", "en", "Which document type is it?"}, {"question", "zh-CN", "这是什么文档类型？"},
			{"source", "", docTypeObject + ".list"}, {"value_field", "", "code"}, {"label_fields", "", "name"}},
			[][2]string{{oSelected, "040"}}},
		{"040", "Ablage wählen", kSelect, "file_account", [][3]string{
			{"question", "de", "In welche Ablage gehört es?"}, {"question", "en", "Which file account does it belong to?"}, {"question", "zh-CN", "归档到哪个位置？"},
			{"source", "", accountCCObject + ".list"}, {"filter.company_code", "", "{company_code}"}, {"filter.leaf", "", "true"}, {"value_field", "", "number"}, {"label_fields", "", "number"}},
			[][2]string{{oSelected, "050"}}},
		{"050", "Geschäftspartner wählen", kSelect, "partner_id", [][3]string{
			{"question", "de", "Von wem ist es (Geschäftspartner)?"}, {"question", "en", "Who sent it (business partner)?"}, {"question", "zh-CN", "来自哪个业务伙伴？"},
			{"source", "", "BusinessPartner.list"}, {"value_field", "", "id"}, {"label_fields", "", "search_term, name1"}, {"optional", "", "true"}},
			[][2]string{{oSelected, "060"}}},
		{"060", "Dokumentdatum", kDate, "doc_date", [][3]string{
			{"question", "de", "Welches Datum trägt das Dokument?"}, {"question", "en", "What is the document date?"}, {"question", "zh-CN", "文档日期是哪天？"}},
			[][2]string{{oEntered, "070"}}},
		{"070", "Titel", kText, "title", [][3]string{
			{"question", "de", "Titel des Dokuments?"}, {"question", "en", "Document title?"}, {"question", "zh-CN", "文档标题？"}},
			[][2]string{{oEntered, "080"}}},
		{"080", "Ablegen bestätigen", kConfirm, "", [][3]string{
			{"question", "de", "Kopfdaten prüfen – jetzt ablegen?"}, {"question", "en", "Check the header – file it now?"}, {"question", "zh-CN", "检查标题数据——现在归档吗？"}},
			[][2]string{{oYes, "090"}}},
		{"090", "Ablegen", kFile, "", nil, nil},
	}
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.set.Entity(workflowObject).Create(ctx, map[string]any{"data": map[string]any{"code": standardWorkflow,
			"name":        "Standard: erkennen, zuordnen, ablegen",
			"description": "Text erkennen, Datum suchen, Dokumentart, Ablage, Partner, Datum und Titel erfragen (Vorgaben des Eingangsverzeichnisses überspringen die Fragen), bestätigen, ablegen."}}); err != nil {
			return err
		}
		for _, s := range steps {
			if _, err := m.db.Exec(ctx, `INSERT INTO dms__step (workflow, step, name, kind, target, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
				standardWorkflow, s.step, s.name, s.kind, nilIfEmpty(s.target), true); err != nil {
				return err
			}
			for _, p := range s.params {
				if _, err := m.db.Exec(ctx, `INSERT INTO dms__step_param (workflow, step, param, locale, value, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
					standardWorkflow, s.step, p[0], p[1], p[2], true); err != nil {
					return err
				}
			}
			for _, o := range s.options {
				if _, err := m.db.Exec(ctx, `INSERT INTO dms__step_option (workflow, step, outcome, value, next_step, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
					standardWorkflow, s.step, o[0], "", nilIfEmpty(o[1]), true); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return err == nil, err
}
