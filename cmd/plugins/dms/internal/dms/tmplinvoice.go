package dms

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Eingangsrechnung aus Vorlage (Schrittart TEMPLATE_INVOICE): Gehört das
// Dokument zu einem Vertrag mit Rechnungen aus Vorlagen (z. B. Rahmenvertrag)
// und ist die Kategorie eine mit Eingangsrechnungen, bietet der Schritt die
// Vorlagen des Vertrags an; mit der Rechnungsnummer des Lieferanten entsteht
// die Rechnung offen in der Beschaffung (SupplierInvoice.fromTemplate) und wird
// Bezug des Dokuments. Der folgende Schritt verlinkt sie zum Bearbeiten
// (Parameter open_ref): Positionen ergänzen, Rechnungsbetrag eintragen.

const kTemplateInvoice = "TEMPLATE_INVOICE"

// templateContract: Vertrag des Dokuments mit Rechnungsvorlagen ("" = keiner).
func (m *Module) templateContract(ctx context.Context, d *docRow) string {
	objs, _ := m.docObjects(ctx, d)
	invoices := false
	for _, o := range objs {
		invoices = invoices || o.Object == "SupplierInvoice"
	}
	if !invoices {
		return ""
	}
	refs, _ := m.activeRefs(ctx, d.ID)
	for _, r := range refs {
		if r.Object == "SupplierInvoice" {
			return "" // Rechnung schon da
		}
	}
	for _, r := range refs {
		if r.Object != "Contract" {
			continue
		}
		if len(m.contractTemplates(ctx, d.CC, r.Key)) > 0 {
			return r.Key
		}
	}
	return ""
}

// contractTemplates: Vorlagen eines Vertrags (Konditionen mit is_template), Wert Konditionsart|Objekt.
func (m *Module) contractTemplates(ctx context.Context, cc, key string) []choice {
	contract := shortKey(key, cc)
	resp, err := m.services.Call(ctx, "ContractCondition", "list", map[string]any{"query": map[string]any{"company_code": cc, "contract_id": contract}})
	if err != nil {
		return nil
	}
	var l struct {
		Items []map[string]any `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return nil
	}
	var out []choice
	for _, it := range l.Items {
		if crud.Str(it["contract_id"]) != contract || !crud.AsBool(it["is_template"]) {
			continue
		}
		v := crud.Str(it["condition_type"])
		if o := crud.Str(it["object_id"]); o != "" {
			v += "|" + o
		}
		lbl, _ := it["_labels"].(map[string]any)
		label := firstOf(crud.Str(it["note"]), crud.Str(lbl["condition_type"]), v)
		if a := crud.Str(it["account_number"]); a != "" {
			label += " – Konto " + a
		}
		if a := crud.Str(lbl["amount"]); a != "" && !strings.HasPrefix(a, "0.00") {
			label += " – " + a
		}
		out = append(out, choice{v, label})
	}
	return out
}

// templateInvoice: Rechnung aus der Vorlage anlegen und als Bezug setzen.
func (m *Module) templateInvoice(ctx context.Context, d *docRow, template, reference string) (string, error) {
	contract := m.templateContract(ctx, d)
	if contract == "" {
		return "", crud.Invalid("kein Vertrag mit Rechnungsvorlagen am Dokument")
	}
	if strings.TrimSpace(reference) == "" {
		return "", crud.Invalid("Rechnungsnummer des Lieferanten fehlt")
	}
	resp, err := m.services.Call(ctx, "SupplierInvoice", "fromTemplate", map[string]any{"company_code": d.CC,
		"contract_id": shortKey(contract, d.CC), "template": template, "supplier_reference": strings.TrimSpace(reference),
		"invoice_date": firstOf(d.DocDate, today())})
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil || out.ID == "" {
		return "", crud.Invalid("Rechnung nicht angelegt")
	}
	if err := m.insertRef(ctx, d, "SupplierInvoice", out.ID); err != nil {
		return "", err
	}
	return out.ID, nil
}

// openLinks: „… bearbeiten“ – Bezüge des Objects open_ref im neuen Fenster.
func (m *Module) openLinks(ctx context.Context, d *docRow, s *stepDef) []metamodel.FormLink {
	object := s.param(ctx, "open_ref")
	if object == "" {
		return nil
	}
	refs, _ := m.activeRefs(ctx, d.ID)
	var out []metamodel.FormLink
	for _, r := range refs {
		if r.Object == object {
			out = append(out, metamodel.FormLink{Label: firstOf(s.param(ctx, "open_label"), object) + " " + shortKey(r.Key, d.CC) + " öffnen",
				URL: "/open/" + object + "/" + strings.ReplaceAll(r.Key, "|", "%7C")})
		}
	}
	return out
}

// camelName: Name als CamelCase für den Dateinamen („Haus und Grundstücksservice GmbH“ → HausUndGrundstücksserviceGmbH).
func camelName(s string) string {
	var b strings.Builder
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("äöüÄÖÜß", r))
	}) {
		rs := []rune(strings.ToLower(w))
		if len(rs) > 0 {
			rs[0] = []rune(strings.ToUpper(string(rs[0])))[0]
		}
		b.WriteString(string(rs))
	}
	out := []rune(b.String())
	if len(out) > 40 {
		out = out[:40]
	}
	return string(out)
}
