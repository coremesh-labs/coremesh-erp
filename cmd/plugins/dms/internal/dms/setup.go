package dms

import (
	"context"
	"fmt"
	"sort"
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
	if created, err = m.ensureWorkflow(ctx, erpWorkflow); err != nil {
		return sdk.Response{}, err
	}
	if created {
		notes = append(notes, "Workflow "+erpWorkflow+" angelegt")
	}
	return sdk.Response{Payload: map[string]any{"message": "DMS " + cc + ": " + strings.Join(notes, ", ")}}, nil
}

const (
	standardWorkflow = "STANDARD"
	erpWorkflow      = "ERP"
)

// --- mitgelieferte Workflows (Form 2) ------------------------------------------------------

// wfStep ist ein Schritt eines mitgelieferten Workflows.
type wfStep struct {
	step, name, kind string
	set              map[string]any // Spalten des Schritts
	texts            [][3]string    // text_key, locale, value
	conds            []wfCond
	maps             []wfMap
	opts             []wfOpt
}

type wfCond struct{ role, left, op, right, desc string }
type wfMap struct{ role, field, value, target, cat, desc string }
type wfOpt struct{ outcome, next, label string }

// q: Frage deutsch (Spalte question), englisch und chinesisch (Übersetzungen).
func q(de, en, zh string) (map[string]any, [][3]string) {
	return map[string]any{"question": de}, [][3]string{{"question", "en", en}, {"question", "zh-CN", zh}}
}

// with ergänzt Spalten.
func with(base map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func userStep(step, name, kind string, question [3]string, kv ...any) wfStep {
	set, texts := q(question[0], question[1], question[2])
	return wfStep{step: step, name: name, kind: kind, set: with(set, kv...), texts: texts}
}

func progStep(step, name, kind string, kv ...any) wfStep {
	return wfStep{step: step, name: name, kind: kind, set: with(nil, kv...)}
}

func (s wfStep) c(cs ...wfCond) wfStep { s.conds = append(s.conds, cs...); return s }
func (s wfStep) m(ms ...wfMap) wfStep  { s.maps = append(s.maps, ms...); return s }
func (s wfStep) o(os ...wfOpt) wfStep  { s.opts = append(s.opts, os...); return s }

// ocrSteps: Text der Datei – Textdatei lesen; PDF mit Text-Ebene; sonst bei eigenen
// Scans aufbereiten (Datei mit Text-Ebene ersetzt den Inhalt) und lesen, bei
// zugeschickten Scans nur den Text erkennen (Datei bleibt). Weiter mit next.
func ocrSteps(next string) []wfStep {
	return []wfStep{
		progStep("005", "Textdatei lesen", kCommand, "command", "READ_TEXT").o(wfOpt{oSuccess, next, ""}, wfOpt{oSkipped, "010", ""}, wfOpt{oFailed, "010", ""}),
		progStep("010", "Text-Ebene des PDF lesen", kCommand, "command", "PDFTEXT").o(wfOpt{oSuccess, "012", ""}, wfOpt{oSkipped, "014", ""}, wfOpt{oFailed, "014", ""}),
		progStep("012", "Text vorhanden?", kCheck).c(wfCond{condCheck, "{text.chars}", opGe, "20", "weniger als 20 Zeichen – Scan ohne Text-Ebene"}).
			o(wfOpt{oYes, next, ""}, wfOpt{oNo, "014", ""}),
		progStep("014", "Eigener Scan?", kCheck).c(wfCond{condCheck, "{inbox.own_scans}", opEq, "true", "zugeschickter Scan – Datei bleibt unverändert"}).
			o(wfOpt{oYes, "015", ""}, wfOpt{oNo, "017", ""}),
		progStep("015", "Scan (PDF) aufbereiten", kCommand, "command", "PREPARE_PDF").o(wfOpt{oSuccess, "018", ""}, wfOpt{oSkipped, "016", ""}, wfOpt{oFailed, "017", ""}),
		progStep("016", "Scan (Bild) aufbereiten", kCommand, "command", "PREPARE_IMAGE").o(wfOpt{oSuccess, "018", ""}, wfOpt{oSkipped, "017", ""}, wfOpt{oFailed, "017", ""}),
		progStep("017", "Texterkennung (PDF)", kCommand, "command", "OCR_PDF").o(wfOpt{oSuccess, next, ""}, wfOpt{oSkipped, "019", ""}, wfOpt{oFailed, next, ""}),
		progStep("018", "Text des aufbereiteten Scans lesen", kCommand, "command", "PDFTEXT").o(wfOpt{oSuccess, next, ""}, wfOpt{oSkipped, next, ""}, wfOpt{oFailed, next, ""}),
		progStep("019", "Texterkennung (Bild)", kCommand, "command", "OCR_IMAGE").o(wfOpt{oSuccess, next, ""}, wfOpt{oSkipped, next, ""}, wfOpt{oFailed, next, ""}),
	}
}

// dateStep: Dokumentdatum aus dem Text (Erkennungsregel doc_date), direkt gesetzt.
func dateStep(step, next string) wfStep {
	return progStep(step, "Datum im Text suchen", kRecognize, "rules", "doc_date", "apply", applySet).o(wfOpt{oSuccess, next, ""}, wfOpt{oFailed, next, ""})
}

// duplicateSteps (0.14.0): Duplikat suchen (025, gleiche Zahlen im Text wie ein
// vorhandenes Dokument) – fragen (026, mit Link zum Original), endgültig verwerfen
// (027) oder weiter mit next.
func duplicateSteps(next string) []wfStep {
	return []wfStep{
		progStep("025", "Duplikat suchen", kDuplicate, "target", "var", "var_name", "duplicate", "min_score", dupMinPercent).
			o(wfOpt{oYes, "026", ""}, wfOpt{oNo, next, ""}, wfOpt{oFailed, next, ""}),
		userStep("026", "Duplikat?", kInput, [3]string{"Das Dokument stimmt mit {var.duplicate_label} überein – ein Duplikat? Verwerfen löscht die Datei im Eingang endgültig.",
			"The document matches {var.duplicate_label} – a duplicate? Discarding deletes the file in the inbox for good.",
			"该文档与 {var.duplicate_label} 一致——是重复文档吗？丢弃将永久删除收件箱中的文件。"},
			"input_type", inYesNo, "open_object", docObject+"={var.duplicate}", "open_label", "Original").
			o(wfOpt{oYes, "027", "Verwerfen"}, wfOpt{oNo, next, "Kein Duplikat – weiter"}),
		progStep("027", "Duplikat verwerfen", kAction, "action_object", docObject, "action_name", "discard", "changes_data", true).
			o(wfOpt{oSuccess, "", ""}, wfOpt{oFailed, "026", ""}),
	}
}

var (
	qDocType  = [3]string{"Welche Dokumentart ist es?", "Which document type is it?", "这是什么文档类型？"}
	qAccount  = [3]string{"In welche Ablage gehört es?", "Which file account does it belong to?", "归档到哪个位置？"}
	qObject   = [3]string{"Zu welchem Vorgang gehört es?", "Which business object does it belong to?", "属于哪个业务对象？"}
	qPartner  = [3]string{"Von welchem Geschäftspartner ist es?", "Which business partner is it from?", "来自哪个业务伙伴？"}
	qDate     = [3]string{"Welches Datum trägt das Dokument?", "What is the document date?", "文档日期是哪天？"}
	partnerLs = "search_term, name1"
)

// processStep (0.29.0): Vorgang wählen (optional, nur offene Vorgänge des
// Buchungskreises). Ein Vorgang legt die Ablage mit Ordner je Vorgang fest
// (DmsFileAccount.folder_object = Process), sonst geht es ohne weiter.
func processStep(step, next string) wfStep {
	return userStep(step, "Vorgang wählen", kSelect, [3]string{"Gehört das Dokument zu einem offenen Vorgang? Dann kommt es in dessen Ordner.",
		"Does the document belong to an open process? Then it goes into its folder.", "文档是否属于某个未结事项？如是，将放入该事项的文件夹。"},
		"target", "ref", "target_object", "Process", "if_set", ifSetSkip, "source_kind", srcObject, "source_object", "Process",
		"value_field", "id", "label_fields", "process_id, short_name, subject").
		m(wfMap{role: mapFilter, field: "company_code", value: "{company_code}"},
			wfMap{role: mapFilter, field: "view", value: "OFFEN", desc: "nur offene Vorgänge (Sicht des Moduls Vorgänge)"}).
		o(wfOpt{oSelected, next, "Zum Vorgang"}, wfOpt{oSkipped, next, "Ohne Vorgang weiter"}, wfOpt{oEmpty, next, ""})
}

// processSteps: wo der Schritt „Vorgang wählen“ in den Vorlagen bzw. vorhandenen
// Workflows steht (Migration 0.29.0) – vor dem Schritt next.
var processSteps = []struct{ workflow, step, next string }{{standardWorkflow, "028", "030"}, {erpWorkflow, "040", "050"}, {"PRIVAT", "038", "040"}}

// standardSteps: Text, Datum, Vorgang, Dokumentart, Ablage, Objekt, Partner, Datum, Titel, bestätigen, ablegen.
func standardSteps() []wfStep {
	steps := append(ocrSteps("020"), duplicateSteps("028")...)
	return append(steps,
		dateStep("020", "025"),
		processStep("028", "030"),
		userStep("030", "Dokumentart wählen", kSelect, qDocType, "target", "doc_type", "if_set", ifSetSkip, "source_kind", srcObject,
			"source_object", docTypeObject, "value_field", "code", "label_fields", "name").o(wfOpt{oSelected, "040", ""}),
		userStep("040", "Ablage wählen", kSelect, qAccount, "target", "file_account", "if_set", ifSetSkip, "source_kind", srcTree).o(wfOpt{oSelected, "045", ""}),
		userStep("045", "Objekt wählen", kSelect, qObject, "target", "ref", "if_set", ifSetSkip, "source_kind", srcCategory).
			o(wfOpt{oSelected, "050", ""}, wfOpt{oSkipped, "050", "Ohne Vorgang weiter"}),
		userStep("050", "Geschäftspartner wählen", kSelect, qPartner, "target", "partner_id", "if_set", ifSetSkip, "source_kind", srcObject,
			"source_object", "BusinessPartner", "value_field", "id", "label_fields", partnerLs).
			o(wfOpt{oSelected, "060", ""}, wfOpt{oSkipped, "060", "Ohne Partner weiter"}),
		userStep("060", "Dokumentdatum", kInput, qDate, "target", "doc_date", "if_set", ifSetSkip, "input_type", inDate).o(wfOpt{oEntered, "070", ""}),
		userStep("070", "Titel", kInput, [3]string{"Titel des Dokuments?", "Document title?", "文档标题？"}, "target", "title", "if_set", ifSetSkip, "input_type", inText).
			o(wfOpt{oEntered, "080", ""}, wfOpt{oSkipped, "080", "Ohne Titel weiter"}),
		userStep("080", "Ablegen bestätigen", kInput, [3]string{"Kopfdaten prüfen – jetzt ablegen?", "Check the header – file it now?", "检查标题数据——现在归档吗？"},
			"input_type", inYesNo).o(wfOpt{oYes, "090", "Ablegen"}),
		progStep("090", "Ablegen", kAction, "action_object", docObject, "action_name", "file", "changes_data", true).o(wfOpt{oSuccess, "", ""}, wfOpt{oFailed, "080", ""}),
	)
}

// partnerContext: Kontext des Schritts „Geschäftspartner anlegen“ mit Hinweisen aus Dokument und Kontoauszug.
const partnerContext = "Der Absender des Dokuments ist der gesuchte Geschäftspartner (nicht die Hausverwaltung als Empfänger)." +
	"|Zahlungen an die IBAN {hint.iban} gingen laut Kontoauszug an: {hint.payee}|Steuernummer des Absenders: {hint.tax_number}"

const creditorDesc = "nur Partner mit Kreditor-Rolle"

// recognizeRules: Erkennungsregeln für Partner und Vertrag.
const recognizeRules = "m_iban, m_phone, contract, m_name1, m_name2, m_short, m_payee_name, payee"

// erpSteps: Ablauf mit angeschlossenem ERP – Kategorie, Partner (erkannt oder gewählt,
// sonst mit KI anlegen), Rechnung aus Vorlage, kaufmännisches Objekt (gewählt oder mit KI
// angelegt), Übernahme, Dokumentart, Rechnung mit KI, Ablage, Datum, Name, ablegen.
func erpSteps() []wfStep {
	steps := append(ocrSteps("020"), duplicateSteps("030")...)
	return append(steps,
		dateStep("020", "025"),
		progStep("030", "Partner und Vertrag erkennen", kRecognize, "rules", recognizeRules, "min_score", 3, "min_gap", 2, "apply", applyPropose).
			o(wfOpt{oSuccess, "040", ""}, wfOpt{oFailed, "040", ""}),
		processStep("040", "050"),
		userStep("050", "Dokumentkategorie wählen", kSelect, [3]string{"Wozu gehört das Dokument (Vertrag, Rechnung, Gebäude …)?",
			"What does the document belong to (contract, invoice, building …)?", "文档属于什么（合同、发票、建筑……）？"},
			"target", "doc_category", "if_set", ifSetSkip, "source_kind", srcObject, "source_object", categoryObject, "value_field", "code", "label_fields", "name").
			o(wfOpt{oSelected, "060", ""}),
		userStep("060", "Geschäftspartner wählen", kSelect, qPartner, "target", "partner_id", "if_set", ifSetSkip, "source_kind", srcObject,
			"source_object", "BusinessPartner", "value_field", "id", "label_fields", partnerLs).
			c(wfCond{condApplies, "{category.partner}", opEq, "true", "entfällt für Dokumentkategorie {doc.doc_category}"}).
			m(wfMap{mapFilter, "role_kind", "CREDITOR", "", "INVOICE", creditorDesc}, wfMap{mapFilter, "role_kind", "CREDITOR", "", "RECEIPT", creditorDesc}).
			o(wfOpt{oSelected, "075", "Partner übernehmen"}, wfOpt{oCreate, "070", "GP anlegen"}, wfOpt{oSkipped, "075", "Ohne Partner weiter"}),
		userStep("070", "Geschäftspartner anlegen", kForm, [3]string{"Neuer Geschäftspartner aus dem Dokument", "New business partner from the document", "从文档新建业务伙伴"},
			"target", "partner_id", "context", partnerContext, "confirm", true, "file_input", true, "file_types", "application/pdf, image/*").
			m(wfMap{role: mapRequest, field: "BusinessPartner", value: aiBusinessPartner},
				wfMap{role: mapDefault, field: "roles.company_codes", value: "{company_code}"},
				wfMap{role: mapDefault, field: "addresses.address.country", value: "DE"},
				wfMap{role: mapHint, field: "partner.name1", value: "{hint.payee}"},
				wfMap{role: mapHint, field: "banks.iban", value: "{hint.iban}"},
				wfMap{role: mapHint, field: "banks.account_holder", value: "{hint.payee}"}).
			o(wfOpt{oCreated, "075", ""}),
		userStep("075", "Vertrag bestätigen", kSelect, [3]string{"Gehört die Rechnung zu diesem Vertrag?", "Does the invoice belong to this contract?", "发票属于这个合同吗？"},
			"target", "ref", "target_object", "Contract", "source_kind", srcObject, "source_object", "Contract", "value_field", "id",
			"label_fields", "contract_id, designation").
			c(wfCond{condApplies, "{proposal.ref:Contract}", opNotEmpty, "", "kein Vertrag erkannt"},
				wfCond{condApplies, "{ref.Contract}", opEmpty, "", "Vertrag schon zugeordnet"},
				wfCond{condApplies, "{category.objects}", opHas, "SupplierInvoice", "Kategorie ohne Eingangsrechnungen"}).
			m(wfMap{role: mapFilter, field: "company_code", value: "{company_code}"},
				wfMap{role: mapFilter, field: "partner_id", value: "{doc.partner_id}", desc: "nur Verträge des Partners"}).
			o(wfOpt{oSelected, "080", "Vertrag übernehmen"}, wfOpt{oSkipped, "080", "Ohne Vertrag weiter"}),
		userStep("080", "Rechnungsvorlage des Vertrags", kSelect, [3]string{"Rechnung zum Vertrag {refid.Contract}: aus welcher Vorlage erfassen?",
			"Invoice for contract {refid.Contract}: which template?", "合同 {refid.Contract} 的发票：使用哪个模板？"},
			"target", "var", "var_name", "template", "source_kind", srcObject, "source_object", "ContractCondition",
			"value_field", "{condition_type}|{object_id}|{position}", "label_fields", "note, condition_type, account_number").
			c(wfCond{condApplies, "{category.objects}", opHas, "SupplierInvoice", "Kategorie ohne Eingangsrechnungen"},
				wfCond{condApplies, "{ref.Contract}", opNotEmpty, "", "kein Vertrag am Dokument"},
				wfCond{condApplies, "{ref.SupplierInvoice}", opEmpty, "", "Rechnung schon zugeordnet"}).
			m(wfMap{role: mapFilter, field: "company_code", value: "{company_code}"}, wfMap{role: mapFilter, field: "contract_id", value: "{refid.Contract}"},
				wfMap{role: mapFilter, field: "is_template", value: "true", desc: "nur Rechnungsvorlagen"}).
			o(wfOpt{oSelected, "082", ""}, wfOpt{oEmpty, "100", ""}, wfOpt{oSkipped, "100", "Ohne Vorlage weiter"}),
		userStep("082", "Rechnungsnummer", kInput, [3]string{"Rechnungsnummer des Lieferanten (Vorlage {var.template_label})?",
			"Supplier invoice number (template {var.template_label})?", "供应商发票号（模板 {var.template_label}）？"},
			"target", "var", "var_name", "invoice_ref", "input_type", inText).o(wfOpt{oEntered, "084", ""}),
		progStep("084", "Rechnung aus Vorlage anlegen", kAction, "action_object", "SupplierInvoice", "action_name", "fromTemplate", "changes_data", true, "apply", applySet).
			m(wfMap{role: mapParam, field: "company_code", value: "{company_code}"}, wfMap{role: mapParam, field: "contract_id", value: "{refid.Contract}"},
				wfMap{role: mapParam, field: "template", value: "{var.template}"}, wfMap{role: mapParam, field: "supplier_reference", value: "{var.invoice_ref}"},
				wfMap{role: mapParam, field: "invoice_date", value: "{doc.doc_date}"},
				wfMap{role: mapResult, field: "id", target: "ref:SupplierInvoice"}).
			o(wfOpt{oSuccess, "090", ""}, wfOpt{oFailed, "100", ""}),
		userStep("090", "Rechnung prüfen und bearbeiten", kInput, [3]string{"Rechnung im neuen Fenster prüfen und bearbeiten (Positionen, Rechnungsbetrag laut Beleg) – fertig?",
			"Check and edit the invoice in the new window (lines, invoice amount) – done?", "在新窗口中检查并编辑发票（行项目、发票金额）——完成了吗？"},
			"input_type", inYesNo, "open_object", "SupplierInvoice", "open_label", "Eingangsrechnung").
			c(wfCond{condApplies, "{ref.SupplierInvoice}", opNotEmpty, "", "keine Eingangsrechnung zugeordnet"}).
			o(wfOpt{oYes, "100", "Fertig"}),
		userStep("100", "Kaufmännisches Objekt wählen", kSelect, qObject, "target", "ref", "if_set", ifSetSkip, "source_kind", srcCategory).
			o(wfOpt{oSelected, "130", ""}, wfOpt{oSkipped, "130", "Ohne Vorgang weiter"}, wfOpt{oCreate, "110", "Vorgang anlegen"}),
		userStep("110", "Vertragsart wählen", kSelect, [3]string{"Welche Vertragsart hat der neue Vertrag?", "Which contract type does the new contract have?", "新合同属于哪种合同类型？"},
			"target", "var", "var_name", "contract_type", "source_kind", srcObject, "source_object", "ContractType", "value_field", "code", "label_fields", "name").
			c(wfCond{condApplies, "{category.objects}", opHas, "Contract", "entfällt für Dokumentkategorie {doc.doc_category}"}).
			m(wfMap{role: mapFilter, field: "company_code", value: "{company_code}"}).
			o(wfOpt{oSelected, "120", ""}, wfOpt{oSkipped, "120", ""}),
		userStep("120", "Vorgang mit KI anlegen", kForm, [3]string{"Neuer Vorgang aus dem Dokument", "New business object from the document", "从文档新建业务对象"},
			"target", "ref", "context", "Geschäftspartner des Dokuments: {partner}|Vertragsart: {var.contract_type_label}").
			m(wfMap{role: mapRequest, field: "SupplierInvoice", value: aiSupplierInvoice}, wfMap{role: mapRequest, field: "Contract", value: aiSupplierContract},
				wfMap{role: mapRequest, field: "ContractPlan", value: aiEconomicPlan}, wfMap{role: mapRequest, field: "PurchaseQuote", value: aiPurchaseQuote},
				wfMap{role: mapDefault, field: "invoice.company_code", value: "{company_code}"}, wfMap{role: mapDefault, field: "invoice.supplier_id", value: "{doc.partner_id}"},
				wfMap{role: mapDefault, field: "items.company_code", value: "{company_code}"}, wfMap{role: mapDefault, field: "contract.company_code", value: "{company_code}"},
				wfMap{role: mapDefault, field: "contract.contract_type", value: "{var.contract_type}"}, wfMap{role: mapDefault, field: "contract.partner_id", value: "{doc.partner_id}"},
				wfMap{role: mapDefault, field: "conditions.company_code", value: "{company_code}"}, wfMap{role: mapDefault, field: "plan.company_code", value: "{company_code}"},
				wfMap{role: mapDefault, field: "plan.contract_id", value: "{refid.Contract}"}, wfMap{role: mapDefault, field: "invoice.framework_contract", value: "{refid.Contract}"},
				wfMap{role: mapPrefill, field: "SupplierInvoice", value: "SupplierInvoice.contractDraft"},
				wfMap{role: mapDefault, field: "quote.company_code", value: "{company_code}"}, wfMap{role: mapDefault, field: "quote.supplier_id", value: "{doc.partner_id}"}).
			o(wfOpt{oCreated, "130", ""}),
		progStep("130", "Daten übernehmen", kAction, "action_object", docObject, "action_name", "takeover", "changes_data", false).
			o(wfOpt{oSuccess, "140", ""}, wfOpt{oFailed, "140", ""}),
		userStep("140", "Dokumentart wählen", kSelect, [3]string{"Als was wird es am Objekt gespeichert (Dokumentart)?", "How is it stored at the object (document type)?",
			"在对象上以何种文档类型保存？"}, "target", "doc_type", "if_set", ifSetSkip, "source_kind", srcObject, "source_object", docTypeObject,
			"value_field", "code", "label_fields", "name", "auto_single", true).
			m(wfMap{role: mapFilter, field: "category", value: "{doc.doc_category}"}).
			o(wfOpt{oSelected, "141", ""}),
		titleStep(),
		existingInvoiceStep(),
		userStep("145", "Vertragspositionen übernehmen?", kSelect, [3]string{"Rechnung zum Vertrag {refid.Contract}: Positionen aus dem Vertrag übernehmen?",
			"Invoice for contract {refid.Contract}: take over the lines from the contract?", "合同 {refid.Contract} 的发票：从合同引用行项目？"},
			"target", "var", "var_name", "contract_items", "source_kind", srcList).
			c(wfCond{condApplies, "{doctype.ref_kind}", opEq, "INVOICE", "entfällt für Dokumentart {doc.doc_type}"},
				wfCond{condApplies, "{ref.Contract}", opNotEmpty, "", "kein Vertrag am Dokument"},
				wfCond{condApplies, "{ref.SupplierInvoice}", opEmpty, "", "Rechnung schon zugeordnet"}).
			m(wfMap{role: mapValue, field: "YES", value: "Ja – Positionen aus dem Vertrag, die KI liest nur den Kopf"},
				wfMap{role: mapValue, field: "NO", value: "Nein – die KI liest die ganze Rechnung"}).
			o(wfOpt{oSelected, "150", ""}, wfOpt{oSkipped, "150", ""}),
		userStep("150", "Rechnung mit KI anlegen", kForm, [3]string{"Eingangsrechnung aus dem Dokument", "Supplier invoice from the document", "从文档创建供应商发票"},
			"target", "ref", "context", "Dokumentart: {doc.doc_type} (bei Gutschriften Beträge negativ)|Geschäftspartner (Lieferant): {partner}|Vertrag: {refid.Contract}").
			c(wfCond{condApplies, "{doctype.ref_kind}", opEq, "INVOICE", "entfällt für Dokumentart {doc.doc_type}"},
				wfCond{condPrefill, "{var.contract_items}", opEq, "YES", ""}).
			m(wfMap{role: mapRequest, field: "SupplierInvoice", value: aiSupplierInvoice},
				wfMap{role: mapRequestPrefill, field: "SupplierInvoice", value: aiInvoiceHead},
				wfMap{role: mapPrefill, field: "SupplierInvoice", value: "SupplierInvoice.contractDraft"},
				wfMap{role: mapDefault, field: "invoice.company_code", value: "{company_code}"}, wfMap{role: mapDefault, field: "invoice.supplier_id", value: "{doc.partner_id}"},
				wfMap{role: mapDefault, field: "invoice.framework_contract", value: "{refid.Contract}"}, wfMap{role: mapDefault, field: "items.company_code", value: "{company_code}"}).
			o(wfOpt{oCreated, "160", ""}, wfOpt{oSkipped, "160", "Ohne Rechnung weiter"}),
		userStep("160", "Ablage wählen", kSelect, qAccount, "target", "file_account", "if_set", ifSetAsk, "source_kind", srcTree).o(wfOpt{oSelected, "170", ""}),
		userStep("170", "Dokumentdatum", kInput, qDate, "target", "doc_date", "if_set", ifSetSkip, "input_type", inDate).o(wfOpt{oEntered, "180", ""}),
		userStep("180", "Dokumentname bestätigen", kInput, [3]string{"Dokumentname bestätigen", "Confirm the document name", "确认文档名称"},
			"target", "name_override", "input_type", inText, "default_value", "{name_proposal}").o(wfOpt{oEntered, "190", "Bestätigen"}),
		progStep("190", "Ablegen", kAction, "action_object", docObject, "action_name", "file", "changes_data", true).o(wfOpt{oSuccess, "", ""}, wfOpt{oFailed, "180", ""}),
	)
}

// titleStep (0.16.2): Titel bei Dokumenten zu Objekten (Bericht, Plan …) – vorbelegt mit
// dem bisherigen Titel (Übernahme: Bezeichnung des Objekts), z. B. „Prüfbericht Heizung 2026“.
func titleStep() wfStep {
	return userStep("141", "Titel", kInput, [3]string{"Titel des Dokuments (z. B. Prüfbericht Heizung 2026)?",
		"Document title (e.g. heating inspection report 2026)?", "文档标题（例如 2026 年供暖检查报告）？"},
		"target", "title", "if_set", ifSetAsk, "input_type", inText).
		c(wfCond{condApplies, "{doc.doc_category}", opEq, "OBJECT", "entfällt für Dokumentkategorie {doc.doc_category}"}).
		o(wfOpt{oEntered, "142", ""})
}

// existingInvoiceStep (0.12.5): Rechnung zum Vertrag, die es schon gibt (z. B. nach einem
// Neustart des Workflows oder von Hand erfasst), dem Dokument zuordnen statt neu anlegen.
// Schritt 142 (seit 0.17.0): auch gebuchte Rechnungen – ein Dokument kann Anlage einer
// schon gebuchten Rechnung sein (z. B. die Originalrechnung zu einer Weiterberechnung).
var invoiceQuestion = [3]string{"Gibt es die Rechnung schon (auch gebucht, z. B. Anlage zur Rechnung)? Rechnungen von {partner}",
	"Does the invoice already exist (also posted, e.g. an attachment)? Invoices of {partner}", "发票已存在吗（包括已过账，例如附件）？{partner} 的发票"}

const (
	invoiceLabels     = "invoice_id, supplier_reference, invoice_date, invoice_amount, status, framework_contract"
	invoiceStatuses   = "OPEN,DRAFT,POSTED"
	invoiceStatusDesc = "nur Rechnungen, die nicht storniert oder verworfen sind"
)

func existingInvoiceStep() wfStep {
	return userStep("142", "Vorhandene Rechnung wählen", kSelect, invoiceQuestion,
		"target", "ref", "target_object", "SupplierInvoice", "source_kind", srcObject, "source_object", "SupplierInvoice", "value_field", "id",
		"label_fields", invoiceLabels).
		c(wfCond{condApplies, "{doctype.ref_kind}", opEq, "INVOICE", "entfällt für Dokumentart {doc.doc_type}"},
			wfCond{condApplies, "{ref.Contract}", opNotEmpty, "", "kein Vertrag am Dokument"},
			wfCond{condApplies, "{ref.SupplierInvoice}", opEmpty, "", "Rechnung schon zugeordnet"}).
		m(wfMap{role: mapFilter, field: "company_code", value: "{company_code}"},
			wfMap{role: mapFilter, field: "supplier_id", value: "{doc.partner_id}", desc: "nur Rechnungen des Partners"},
			wfMap{role: mapFilter, field: "status", value: invoiceStatuses, desc: invoiceStatusDesc}).
		o(wfOpt{oSelected, "160", "Rechnung zuordnen"}, wfOpt{oSkipped, "145", "Neue Rechnung anlegen"})
}

// builtinWorkflows: mitgelieferte Workflows (Schlüssel → Bezeichnung, Beschreibung, Schritte).
func builtinWorkflows() map[string]struct {
	name, description string
	steps             []wfStep
} {
	return map[string]struct {
		name, description string
		steps             []wfStep
	}{
		standardWorkflow: {"Standard: erkennen, zuordnen, ablegen",
			"Text erkennen, Datum suchen, Dokumentart, Ablage, Objekt, Partner, Datum und Titel erfragen (Vorgaben des Eingangsverzeichnisses überspringen die Fragen), bestätigen, ablegen.",
			standardSteps()},
		erpWorkflow: {"ERP: Kategorie, Objekt, Übernahme, Dokumentart, ablegen",
			"Mit angeschlossenem ERP: Partner und Vertrag erkennen (Vorschläge), Dokumentkategorie, Geschäftspartner, kaufmännisches Objekt wählen oder mit KI anlegen, Partner, Titel und Ablage (über das Sachkonto) übernehmen, Dokumentart am Objekt, Dokumentname bestätigen, ablegen.",
			erpSteps()},
	}
}

func (m *Module) ensureStandardWorkflow(ctx context.Context) (bool, error) {
	return m.ensureWorkflow(ctx, standardWorkflow)
}

// ensureWorkflow legt einen mitgelieferten Workflow an, falls er fehlt.
func (m *Module) ensureWorkflow(ctx context.Context, code string) (bool, error) {
	if _, err := m.set.Entity(workflowObject).Load(ctx, crud.Record{"code": code}); err == nil {
		return false, nil
	}
	return true, m.db.InTx(ctx, nil, func(ctx context.Context) error {
		b := builtinWorkflows()[code]
		if _, err := m.set.Entity(workflowObject).Create(ctx, map[string]any{"data": map[string]any{"code": code,
			"name": b.name, "description": b.description, "format": wfFormat}}); err != nil {
			return err
		}
		return m.insertSteps(ctx, code, b.steps)
	})
}

// replaceSteps: Schritte eines Workflows durch die mitgelieferten ersetzen (Übernahme in Form 2).
func (m *Module) replaceSteps(ctx context.Context, code string, steps []wfStep) error {
	for _, t := range []string{"dms__step_option", "dms__step_param", "dms__step_condition", "dms__step_mapping", "dms__step_text", "dms__step"} {
		if _, err := m.db.Exec(ctx, `DELETE FROM `+t+` WHERE workflow = ?`, code); err != nil {
			return err
		}
	}
	if err := m.insertSteps(ctx, code, steps); err != nil {
		return err
	}
	_, err := m.db.Exec(ctx, `UPDATE dms__workflow SET format = ?, start_step = NULL WHERE code = ?`, wfFormat, code)
	return err
}

func (m *Module) insertSteps(ctx context.Context, code string, steps []wfStep) error {
	for _, s := range steps {
		active := true
		if v, ok := s.set["is_active"].(bool); ok {
			active = v
		}
		cols := []string{"workflow", "step", "name", "kind", "is_active"}
		vals := []any{code, s.step, s.name, s.kind, active}
		keys := make([]string, 0, len(s.set))
		for k := range s.set {
			if k != "is_active" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			cols, vals = append(cols, k), append(vals, s.set[k])
		}
		ph := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
		if _, err := m.db.Exec(ctx, `INSERT INTO dms__step (`+strings.Join(cols, ", ")+`) VALUES (`+ph+`)`, vals...); err != nil {
			return fmt.Errorf("Schritt %s: %w", s.step, err)
		}
		for i, c := range s.conds {
			if _, err := m.db.Exec(ctx, `INSERT INTO dms__step_condition (workflow, step, line_no, role, left_expr, operator, right_expr, description, is_active)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, code, s.step, (i+1)*10, c.role, c.left, c.op, nilIfEmpty(c.right), nilIfEmpty(c.desc), true); err != nil {
				return err
			}
		}
		for i, p := range s.maps {
			if _, err := m.db.Exec(ctx, `INSERT INTO dms__step_mapping (workflow, step, line_no, role, field, value, target, only_category, description, is_active)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, code, s.step, (i+1)*10, p.role, p.field, nilIfEmpty(p.value), nilIfEmpty(p.target),
				nilIfEmpty(p.cat), nilIfEmpty(p.desc), true); err != nil {
				return err
			}
		}
		for _, t := range s.texts {
			if _, err := m.db.Exec(ctx, `INSERT INTO dms__step_text (workflow, step, text_key, locale, value) VALUES (?, ?, ?, ?, ?)`,
				code, s.step, t[0], t[1], t[2]); err != nil {
				return err
			}
		}
		for _, o := range s.opts {
			if _, err := m.db.Exec(ctx, `INSERT INTO dms__step_option (workflow, step, outcome, value, next_step, label, is_active) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				code, s.step, o.outcome, "", nilIfEmpty(o.next), nilIfEmpty(o.label), true); err != nil {
				return err
			}
		}
	}
	return nil
}
