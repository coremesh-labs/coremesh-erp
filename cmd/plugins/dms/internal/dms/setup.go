package dms

import (
	"context"
	"fmt"
	"sort"
	"strconv"
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
	if created, err = m.ensureWorkflow(ctx, erpWorkflow, "ERP: Kategorie, Objekt, Übernahme, Dokumentart, ablegen",
		"Mit angeschlossenem ERP: Dokumentkategorie, (Geschäftspartner), kaufmännisches Objekt wählen oder anlegen, Partner, Datum, Titel und Ablage (über das Sachkonto) übernehmen, Dokumentart am Objekt, Dokumentname bestätigen, ablegen.",
		erpSteps); err != nil {
		return sdk.Response{}, err
	}
	if created {
		notes = append(notes, "Workflow "+erpWorkflow+" angelegt")
	}
	return sdk.Response{Payload: map[string]any{"message": "DMS " + cc + ": " + strings.Join(notes, ", ")}}, nil
}

const standardWorkflow = "STANDARD"

// objectStepParams: Schritt „Objekt wählen“ – Gebäude, Mietobjekt oder Vertrag als Bezug (optional).
var objectStepParams = [][3]string{
	{"question", "de", "Zu welchem Vorgang gehört es?"},
	{"question", "en", "Which business object does it belong to?"},
	{"question", "zh-CN", "属于哪个业务对象？"},
	{"objects", "", "category"}, {"optional", "", "true"},
}

// ensureStandardWorkflow legt den Standard-Workflow an, falls er fehlt:
// Text erkennen → Datum suchen → Dokumentart → Ablage → Partner → Datum →
// Titel → bestätigen → ablegen. Felder, die das Eingangsverzeichnis vorgibt,
// werden übersprungen.
// wfStep ist ein Schritt eines mitgelieferten Workflows.
type wfStep struct {
	step, name, kind, target string
	params                   [][3]string // param, locale, value
	options                  [][2]string // outcome, next
}

func (m *Module) ensureStandardWorkflow(ctx context.Context) (bool, error) {
	return m.ensureWorkflow(ctx, standardWorkflow, "Standard: erkennen, zuordnen, ablegen",
		"Text erkennen, Datum suchen, Dokumentart, Ablage, Objekt, Partner, Datum und Titel erfragen (Vorgaben des Eingangsverzeichnisses überspringen die Fragen), bestätigen, ablegen.",
		standardSteps)
}

// standardSteps: Text erkennen → Datum → Dokumentart → Ablage → Objekt → Partner → Datum → Titel → bestätigen → ablegen.
var standardSteps = []wfStep{
	{"010", "Text erkennen", kOCR, "", [][3]string{{"command", "", defaultCommand}, {"fallback_command", "", defaultFallbackCommand},
		{"image_command", "", defaultImageCommand}, {"prepare_command", "", defaultPrepareCommand},
		{"prepare_image_command", "", defaultPrepareImageCommand}, {"languages", "", "deu+eng"}, {"min_chars", "", "20"}},
		[][2]string{{oSuccess, "020"}, {oFailed, "030"}}},
	{"020", "Datum im Text suchen", kExtract, "doc_date", [][3]string{{"pattern", "", `(\d{1,2}\.\d{1,2}\.\d{4})`}},
		[][2]string{{oSuccess, "030"}, {oFailed, "030"}}},
	{"030", "Dokumentart wählen", kSelect, "doc_type", [][3]string{
		{"question", "de", "Welche Dokumentart ist es?"}, {"question", "en", "Which document type is it?"}, {"question", "zh-CN", "这是什么文档类型？"},
		{"source", "", docTypeObject + ".list"}, {"value_field", "", "code"}, {"label_fields", "", "name"}},
		[][2]string{{oSelected, "040"}}},
	{"040", "Ablage wählen", kSelect, "file_account", [][3]string{
		{"question", "de", "In welche Ablage gehört es?"}, {"question", "en", "Which file account does it belong to?"}, {"question", "zh-CN", "归档到哪个位置？"},
		{"tree", "", "true"}},
		[][2]string{{oSelected, "045"}}},
	{"045", "Objekt wählen", kSelect, "ref", objectStepParams, [][2]string{{oSelected, "050"}}},
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

// ensureWorkflow legt einen mitgelieferten Workflow an, falls er fehlt.
func (m *Module) ensureWorkflow(ctx context.Context, code, name, description string, steps []wfStep) (bool, error) {
	if _, err := m.set.Entity(workflowObject).Load(ctx, crud.Record{"code": code}); err == nil {
		return false, nil
	}
	standardWorkflow := code
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.set.Entity(workflowObject).Create(ctx, map[string]any{"data": map[string]any{"code": code,
			"name": name, "description": description}}); err != nil {
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

const erpWorkflow = "ERP"

// partnerCategoryFilter (seit 0.11.0): Partnerauswahl je Kategorie – bei Rechnungen und
// Belegen nur Partner mit Kreditor-Rolle (Merkmal der Rollenart).
const partnerCategoryFilter = "INVOICE: role_kind=CREDITOR (nur Partner mit Kreditor-Rolle); RECEIPT: role_kind=CREDITOR (nur Partner mit Kreditor-Rolle)"

// identifyParams (seit 0.11.0): Punkte der Erkennung als Parameter sichtbar und änderbar.
func identifyParams() [][3]string {
	out := [][3]string{{"min_score", "", "3"}, {"min_gap", "", "2"}}
	keys := make([]string, 0, len(defaultScores))
	for k := range defaultScores {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, [3]string{"score." + k, "", strconv.Itoa(defaultScores[k])})
	}
	return out
}

// partnerContextOld: Kontext des Schritts „Geschäftspartner anlegen“ bis 0.9.x (Migration).
const partnerContextOld = "Der Absender des Dokuments ist der gesuchte Geschäftspartner (nicht die Hausverwaltung als Empfänger)."

// partnerStepParams (seit 0.10.0): Kontext mit Hinweisen aus Dokument und Kontoauszug,
// Vorbelegung daraus, Vorschau vor dem Anlegen, Datei an die KI auf Nachfrage.
var partnerStepParams = [][3]string{
	{"context", "", partnerContextOld + "|Zahlungen an die IBAN {hint.iban} gingen laut Kontoauszug an: {hint.payee}|Steuernummer des Absenders: {hint.tax_number}"},
	{"hints", "", "partner.name1={hint.payee}; banks.iban={hint.iban}; banks.account_holder={hint.payee}"},
	{"confirm", "", "true"},
	{"file_input", "", "ask"},
	{"file_types", "", "application/pdf, image/*"},
}

// erpSteps: Ablauf mit angeschlossenem ERP (siehe erp.go).
var erpSteps = []wfStep{
	{"010", "Text erkennen", kOCR, "", [][3]string{{"command", "", defaultCommand}, {"fallback_command", "", defaultFallbackCommand},
		{"image_command", "", defaultImageCommand}, {"prepare_command", "", defaultPrepareCommand},
		{"prepare_image_command", "", defaultPrepareImageCommand}, {"languages", "", "deu+eng"}, {"min_chars", "", "20"}},
		[][2]string{{oSuccess, "020"}, {oFailed, "030"}}},
	{"020", "Datum im Text suchen", kExtract, "doc_date", [][3]string{{"pattern", "", `(\d{1,2}\.\d{1,2}\.\d{4})`}},
		[][2]string{{oSuccess, "030"}, {oFailed, "030"}}},
	{"030", "Partner und Vertrag erkennen", kIdentify, "", identifyParams(), [][2]string{{oSuccess, "040"}, {oFailed, "050"}}},
	{"040", "Erkennung bestätigen", kConfirm, "", [][3]string{
		{"question", "de", "Erkannt: Partner {partner}, Bezug {refs} – stimmt das?"},
		{"question", "en", "Recognized: partner {partner}, reference {refs} – correct?"},
		{"question", "zh-CN", "已识别：伙伴 {partner}，关联 {refs}——正确吗？"},
		{"when", "", "identified"}, {"undo_to", "", "030"}},
		[][2]string{{oYes, "050"}, {oNo, "050"}}},
	{"050", "Dokumentkategorie wählen", kSelect, "doc_category", [][3]string{
		{"question", "de", "Wozu gehört das Dokument (Vertrag, Rechnung, Gebäude …)?"}, {"question", "en", "What does the document belong to (contract, invoice, building …)?"},
		{"question", "zh-CN", "文档属于什么（合同、发票、建筑……）？"},
		{"source", "", categoryObject + ".list"}, {"value_field", "", "code"}, {"label_fields", "", "name"}},
		[][2]string{{oSelected, "060"}}},
	{"060", "Geschäftspartner wählen", kSelect, "partner_id", [][3]string{
		{"question", "de", "Von welchem Geschäftspartner ist es?"}, {"question", "en", "Which business partner is it from?"}, {"question", "zh-CN", "来自哪个业务伙伴？"},
		{"source", "", "BusinessPartner.list"}, {"value_field", "", "id"}, {"label_fields", "", "search_term, name1"},
		{"when", "", "category_partner"}, {"category_filter", "", partnerCategoryFilter}},
		[][2]string{{oSelected, "080"}, {oCreate, "070"}}},
	{"070", "Geschäftspartner anlegen", kAIObject, "", append([][3]string{
		{"question", "de", "Neuer Geschäftspartner aus dem Dokument"}, {"question", "en", "New business partner from the document"},
		{"question", "zh-CN", "从文档新建业务伙伴"},
		{"requests", "", "BusinessPartner=" + aiBusinessPartner}, {"bind", "", "partner"},
		{"defaults", "", "roles.company_codes={company_code}; addresses.address.country=DE"}}, partnerStepParams...),
		[][2]string{{oSelected, "080"}}},
	{"080", "Eingangsrechnung aus Vorlage", kTemplateInvoice, "", [][3]string{
		{"question", "de", "Rechnung zum Vertrag {refs}: aus welcher Vorlage erfassen?"}, {"question", "en", "Invoice for contract {refs}: which template?"},
		{"question", "zh-CN", "合同 {refs} 的发票：使用哪个模板？"}, {"when", "", "contract_templates"}},
		[][2]string{{oSelected, "090"}, {oSkipped, "100"}}},
	{"090", "Rechnung prüfen und bearbeiten", kConfirm, "", [][3]string{
		{"question", "de", "Rechnung im neuen Fenster prüfen und bearbeiten (Positionen, Rechnungsbetrag laut Beleg) – fertig?"},
		{"question", "en", "Check and edit the invoice in the new window (lines, invoice amount) – done?"},
		{"question", "zh-CN", "在新窗口中检查并编辑发票（行项目、发票金额）——完成了吗？"},
		{"when", "", "has_invoice"}, {"open_ref", "", "SupplierInvoice"}, {"open_label", "", "Eingangsrechnung"}},
		[][2]string{{oYes, "100"}, {oNo, "100"}}},
	{"100", "Kaufmännisches Objekt wählen", kSelect, "ref", [][3]string{
		{"question", "de", "Zu welchem Vorgang gehört es?"}, {"question", "en", "Which business object does it belong to?"}, {"question", "zh-CN", "属于哪个业务对象？"},
		{"objects", "", "category"}, {"optional", "", "true"}},
		// übersprungen, weil die Erkennung (022) den Bezug schon gesetzt hat: Daten trotzdem übernehmen
		// (Ablage über die Sachkonten); ohne Bezug scheitert 050 und es geht mit 055 weiter.
		[][2]string{{oSelected, "130"}, {oSkipped, "130"}, {oCreate, "110"}}},
	{"110", "Vertragsart wählen", kSelect, "var", [][3]string{
		{"question", "de", "Welche Vertragsart hat der neue Vertrag?"}, {"question", "en", "Which contract type does the new contract have?"},
		{"question", "zh-CN", "新合同属于哪种合同类型？"},
		{"var", "", "contract_type"}, {"source", "", "ContractType.list"}, {"filter.company_code", "", "{company_code}"},
		{"value_field", "", "code"}, {"label_fields", "", "name"}, {"when", "", "object:Contract"}},
		[][2]string{{oSelected, "120"}, {oSkipped, "120"}}},
	{"120", "Vorgang mit KI anlegen", kAIObject, "", [][3]string{
		{"question", "de", "Neuer Vorgang aus dem Dokument"}, {"question", "en", "New business object from the document"},
		{"question", "zh-CN", "从文档新建业务对象"},
		{"requests", "", "SupplierInvoice=" + aiSupplierInvoice + "; Contract=" + aiSupplierContract + "; ContractPlan=" + aiEconomicPlan},
		{"bind", "", "ref"},
		{"context", "", "Geschäftspartner des Dokuments: {partner}|Vertragsart: {var.contract_type_label}"},
		{"defaults", "", "invoice.company_code={company_code}; invoice.supplier_id={doc.partner_id}; items.company_code={company_code}; " +
			"contract.company_code={company_code}; contract.contract_type={var.contract_type}; contract.partner_id={doc.partner_id}; " +
			"conditions.company_code={company_code}; plan.company_code={company_code}; plan.contract_id={refid.Contract}; " +
			"invoice.framework_contract={refid.Contract}"},
		{"prefill", "", "SupplierInvoice=SupplierInvoice.contractDraft"}},
		[][2]string{{oSelected, "130"}}},
	{"130", "Daten übernehmen", kTakeover, "", nil, [][2]string{{oSuccess, "140"}, {oFailed, "140"}}},
	{"140", "Dokumentart wählen", kSelect, "doc_type", [][3]string{
		{"question", "de", "Als was wird es am Objekt gespeichert (Dokumentart)?"}, {"question", "en", "How is it stored at the object (document type)?"},
		{"question", "zh-CN", "在对象上以何种文档类型保存？"},
		{"source", "", docTypeObject + ".list"}, {"filter.category", "", "{doc.doc_category}"}, {"value_field", "", "code"}, {"label_fields", "", "name"},
		{"auto_single", "", "true"}},
		[][2]string{{oSelected, "145"}}},
	{"145", "Vertragspositionen übernehmen?", kSelect, "var", [][3]string{
		{"question", "de", "Rechnung zum Vertrag {refid.Contract}: Positionen aus dem Vertrag übernehmen?"},
		{"question", "en", "Invoice for contract {refid.Contract}: take over the lines from the contract?"},
		{"question", "zh-CN", "合同 {refid.Contract} 的发票：从合同引用行项目？"},
		{"var", "", "contract_items"},
		{"options", "", "YES=Ja – Positionen aus dem Vertrag, die KI liest nur den Kopf; NO=Nein – die KI liest die ganze Rechnung"},
		{"when", "", "ref_kind:INVOICE & ref:Contract & no_ref:SupplierInvoice"}},
		[][2]string{{oSelected, "150"}, {oSkipped, "150"}}},
	{"150", "Rechnung mit KI anlegen", kAIObject, "", [][3]string{
		{"question", "de", "Eingangsrechnung aus dem Dokument"}, {"question", "en", "Supplier invoice from the document"},
		{"question", "zh-CN", "从文档创建供应商发票"},
		{"when", "", "ref_kind:INVOICE"}, {"optional", "", "true"},
		{"requests", "", "SupplierInvoice=" + aiSupplierInvoice}, {"bind", "", "ref"},
		{"context", "", "Dokumentart: {doc.doc_type} (bei Gutschriften Beträge negativ)|Geschäftspartner (Lieferant): {partner}|Vertrag: {refid.Contract}"},
		{"defaults", "", "invoice.company_code={company_code}; invoice.supplier_id={doc.partner_id}; invoice.framework_contract={refid.Contract}; items.company_code={company_code}"},
		{"prefill", "", "SupplierInvoice=SupplierInvoice.contractDraft"}, {"prefill_when", "", "var:contract_items=YES"},
		{"requests_prefill", "", "SupplierInvoice=" + aiInvoiceHead}},
		[][2]string{{oSelected, "160"}, {oSkipped, "160"}}},
	{"160", "Ablage wählen", kSelect, "file_account", [][3]string{
		{"question", "de", "In welche Ablage gehört es?"}, {"question", "en", "Which file account does it belong to?"}, {"question", "zh-CN", "归档到哪个位置？"},
		{"tree", "", "true"}},
		[][2]string{{oSelected, "170"}}},
	{"170", "Dokumentdatum", kDate, "doc_date", [][3]string{
		{"question", "de", "Welches Datum trägt das Dokument?"}, {"question", "en", "What is the document date?"}, {"question", "zh-CN", "文档日期是哪天？"}},
		[][2]string{{oEntered, "180"}}},
	{"180", "Dokumentname bestätigen", kName, "", [][3]string{
		{"question", "de", "Dokumentname bestätigen"}, {"question", "en", "Confirm the document name"}, {"question", "zh-CN", "确认文档名称"}},
		[][2]string{{oEntered, "190"}}},
	{"190", "Ablegen", kFile, "", nil, nil},
}
