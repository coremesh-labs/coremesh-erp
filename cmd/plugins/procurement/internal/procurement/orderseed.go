package procurement

import "context"

// Vorschlagswerte der Auftragserteilung je Buchungskreis (nur fehlende Einträge;
// danach in den Einstellungen änderbar).

var defaultItemKinds = []struct {
	code, name, short    string
	inTotal, preselected bool
}{
	{"NORMAL", "Normalposition", "", true, true},
	{"ALT", "Alternativposition", "Alt.", false, false},
	{"BED", "Bedarfsposition (Eventualposition)", "Bed.", false, false},
}

var defaultOrderTypes = []struct {
	code, name, template, docType, roles, sigType, subject, mailSubject, mailBlock, mailCategory string
	showItems                                                                                    bool
}{
	{"AU", "Auftrag an Handwerker", "", "PURCHASE_ORDER", "SEADM,OWNER", "SIGNATURE", "Auftragserteilung",
		"Auftragserteilung {auftrag:description} – {objekt:text}", "MAIL", "EMAIL", true},
}

var defaultTextBlocks = []struct {
	code, name, section, text string
	isDefault                 bool
	sort                      int
}{
	{"AUFTRAG", "Auftrag gemäß Angebot", sectionIntro,
		"hiermit erteile ich Ihnen den Auftrag gemäß Ihrem Angebot {angebote} für die dort näher beschriebenen Leistungen.", true, 10},
	{"GESPRAECH", "Dank für das Gespräch", sectionIntro,
		"vielen Dank für das gute und informative Gespräch bei der Besichtigung.", false, 5},
	{"SUMME", "Vertragssumme", sectionIntro,
		"Nach Prüfung beträgt die vorläufige Vertragssumme {summe:netto} Euro zuzüglich der gesetzlichen Umsatzsteuer in der jeweils geltenden Höhe.", true, 20},
	{"FESTPREIS", "Festpreise", sectionIntro,
		"Die Einheitspreise des Angebots sind Festpreise, sofern nicht Pauschalpreise, und gelten während der gesamten Bauzeit.", true, 30},
	{"VEREINBART", "Einleitung der Vereinbarungen", sectionIntro,
		"Im Rahmen unserer Vertragsverhandlungen haben wir folgendes vereinbart:", true, 40},
	{"UMFANG", "Angebot im vollen Umfang", sectionAgreement,
		"Das Angebot {angebot:supplier_reference} setzen Sie bitte im vollen Umfang um.", false, 100},
	{"TERMIN", "Ausführungszeitraum", sectionAgreement,
		"Die Ausführung ist vom {auftrag:planned_from} bis {auftrag:planned_to} vorgesehen.", false, 200},
	{"VOB", "VOB Teile B und C", sectionAgreement,
		"Die VOB Teile B und C sind Vertragsbestandteil, und zwar in der derzeit geltenden Fassung.", true, 900},
	{"MAIL", "Text der E-Mail", sectionMail,
		"Sehr geehrte Damen und Herren,\n\nanbei erhalten Sie den Auftrag {auftrag:number} zu Ihrem Angebot {angebote}.\n\nMit freundlichen Grüßen\n{absender:name}", false, 1000},
}

var defaultContactRoles = []struct{ code, name, text string }{
	{"SCHLUESSEL", "Schlüssel", "Die Schlüssel sind bei {kontakt:name} hinterlegt."},
	{"ABSTIMMUNG", "Abstimmung mit anderen Gewerken", "Bitte stimmen Sie sich bei den Arbeiten mit {kontakt:name} ab."},
	{"VOR_ORT", "Ansprechpartner vor Ort", "Ansprechpartner vor Ort ist {kontakt:name}."},
	{"ZUGANG", "Zugang zur Wohnung (Mieter)", "Den Zugang zur Wohnung vereinbaren Sie bitte mit {kontakt:name}."},
}

// setupOrderCatalogs legt fehlende Positionsarten, Auftragsarten, Textbausteine und
// Kontaktrollen eines Buchungskreises an; liefert die Zahl der neuen Einträge.
func (m *Module) setupOrderCatalogs(ctx context.Context, cc string) (int, error) {
	n := 0
	insert := func(table, code, cols string, args ...any) error {
		if m.text(ctx, "SELECT code FROM "+table+" WHERE company_code = ? AND code = ?", cc, code) != "" {
			return nil
		}
		marks := "?"
		for range args {
			marks += ", ?"
		}
		if _, err := m.db.Exec(ctx, "INSERT INTO "+table+" (company_code, "+cols+") VALUES ("+marks+")", append([]any{cc}, args...)...); err != nil {
			return err
		}
		n++
		return nil
	}
	for i, k := range defaultItemKinds {
		if err := insert("procurement__item_kind", k.code, "code, name, short, in_total, preselected, sort_order, is_active",
			k.code, k.name, nilIfEmpty(k.short), k.inTotal, k.preselected, (i+1)*10, true); err != nil {
			return n, err
		}
	}
	for _, b := range defaultTextBlocks {
		if err := insert("procurement__text_block", b.code, "code, name, section, text, is_default, sort_order, is_active",
			b.code, b.name, b.section, b.text, b.isDefault, b.sort, true); err != nil {
			return n, err
		}
	}
	for i, t := range defaultOrderTypes {
		if err := insert("procurement__order_type", t.code, "code, name, template, doc_type, sender_roles, signature_doc_type, subject, mail_subject, mail_block, mail_category, show_items, sort_order, is_active",
			t.code, t.name, nilIfEmpty(t.template), t.docType, t.roles, t.sigType, t.subject, t.mailSubject, t.mailBlock, t.mailCategory, t.showItems, (i+1)*10, true); err != nil {
			return n, err
		}
	}
	for i, r := range defaultContactRoles {
		if err := insert("procurement__contact_role", r.code, "code, name, text, sort_order, is_active", r.code, r.name, r.text, (i+1)*10, true); err != nil {
			return n, err
		}
	}
	return n, nil
}

// migrateOrderCatalogs (seit 0.14.0, wiederholbar): Buchungskreise mit Rechnungsarten bekommen die
// Vorschlagswerte der Auftragserteilung (fehlende Einträge).
func (m *Module) migrateOrderCatalogs(ctx context.Context) {
	res, err := m.db.Query(ctx, `SELECT DISTINCT company_code FROM procurement__invoice_type`)
	if err != nil {
		return // Schema noch nicht angelegt
	}
	for _, r := range res.Rows {
		cc, _ := r[0].(string)
		if cc == "" {
			continue
		}
		if _, err := m.setupOrderCatalogs(ctx, cc); err != nil {
			m.log.WarnContext(ctx, "Vorschlagswerte der Auftragserteilung nicht angelegt", "company_code", cc, "err", err.Error())
		}
	}
}
