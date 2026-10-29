package reportapi

import (
	"context"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/textservice"
)

// Partner: Angaben eines Geschäftspartners für Briefe und Berichte – nur
// Stammdaten, keine festen Texte:
//
//	{id, type, name, first_name, last_name, title (Anrede, z. B. Frau),
//	 letter_salutation, gender, street, zip, city, country,
//	 contact: {Kategorie bzw. Kommunikationstyp: Wert, z. B. PHONE, EMAIL, MOBILE},
//	 contacts: [{type, category, value}],
//	 bank: {name, iban, bic, holder},
//	 texts: {Textkategorie: Inhalt} – Texte am Partner (Textmanagement), z. B. SIGNATURE}
//
// contact enthält je Kategorie den Standard-Eintrag, sonst den ersten, und je
// Kommunikationstyp den ersten (seit procurement 0.15.5); nil = nicht lesbar.
func Partner(ctx context.Context, s module.Services, id string) map[string]any {
	if id == "" {
		return nil
	}
	resp, err := s.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return nil
	}
	var bp struct {
		Type       string `json:"type"`
		Name1      string `json:"name1"`
		Name2      string `json:"name2"`
		Gender     string `json:"gender"`
		Salutation string `json:"salutation_code"`
		Letter     string `json:"letter_salutation"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return nil
	}
	p := map[string]any{"id": id, "type": bp.Type, "name": DisplayName(ctx, s, id), "gender": bp.Gender, "letter_salutation": bp.Letter}
	if bp.Type == "PERSON" {
		p["first_name"], p["last_name"] = bp.Name2, bp.Name1
	}
	if bp.Salutation != "" {
		if r, err := s.Call(ctx, "PartnerSalutation", "get", map[string]any{"id": bp.Salutation}); err == nil {
			var sal struct {
				Description string `json:"description"`
			}
			if sdk.Decode(r.Payload, &sal) == nil {
				p["title"] = sal.Description
			}
		}
	}
	for k, v := range Address(ctx, s, id) {
		p[k] = v
	}
	p["contact"], p["contacts"] = contacts(ctx, s, id)
	p["bank"] = bank(ctx, s, id)
	p["texts"] = texts(ctx, s, "BusinessPartner", id)
	return p
}

// texts: am Stichtag gültige Texte eines Datensatzes je Kategorie (deutsch,
// sonst die erste Sprache; mehrfache Kategorien durch Leerzeilen getrennt).
func texts(ctx context.Context, s module.Services, entity, id string) map[string]any {
	out := map[string]any{}
	l, err := textservice.New(s).List(ctx, textservice.ListRequest{EntityType: entity, EntityID: id, EffectiveDate: crud.KeyDate(ctx)})
	if err != nil {
		return out
	}
	byCat := map[string][]textservice.Text{}
	for _, t := range l.Items {
		byCat[t.Category] = append(byCat[t.Category], t)
	}
	for c, ts := range byCat {
		pos := map[int]string{}
		var order []int
		for _, t := range ts {
			if _, ok := pos[t.Position]; !ok {
				order = append(order, t.Position)
			}
			if _, ok := pos[t.Position]; !ok || t.Language == "de" {
				pos[t.Position] = strings.TrimSpace(t.Content)
			}
		}
		sort.Ints(order)
		parts := make([]string, len(order))
		for i, p := range order {
			parts[i] = pos[p]
		}
		out[c] = strings.Join(parts, "\n\n")
	}
	return out
}

// Address: Hauptanschrift (Standard, sonst die erste) – {street, zip, city, country}.
func Address(ctx context.Context, s module.Services, id string) map[string]any {
	resp, err := s.Call(ctx, "PartnerAddress", "list", map[string]any{"query": map[string]any{"bp_id": id}})
	if err != nil {
		return nil
	}
	var l struct {
		Items []struct {
			AddressID string `json:"address_id"`
			Role      string `json:"address_role_code"`
			Default   any    `json:"is_default"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil || len(l.Items) == 0 {
		return nil
	}
	best := l.Items[0]
	for _, it := range l.Items {
		if it.Role == "MAIN" && (crud.AsBool(it.Default) || best.Role != "MAIN") {
			best = it
		}
	}
	resp, err = s.Call(ctx, "PartnerAddressData", "get", map[string]any{"id": best.AddressID})
	if err != nil {
		return nil
	}
	var a struct {
		Street  string `json:"street"`
		HouseNo string `json:"house_no"`
		Zip     string `json:"zip_code"`
		City    string `json:"city"`
		Country string `json:"country"`
	}
	if sdk.Decode(resp.Payload, &a) != nil {
		return nil
	}
	return map[string]any{"street": strings.TrimSpace(a.Street + " " + a.HouseNo), "zip": a.Zip, "city": a.City, "country": a.Country}
}

func contacts(ctx context.Context, s module.Services, id string) (map[string]any, []map[string]any) {
	main, all := map[string]any{}, []map[string]any{}
	resp, err := s.Call(ctx, "PartnerContact", "list", map[string]any{"query": map[string]any{"bp_id": id}})
	if err != nil {
		return main, all
	}
	var l struct {
		Items []struct {
			Type    string `json:"comm_type_code"`
			Value   string `json:"value"`
			Default any    `json:"is_default"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return main, all
	}
	category := map[string]string{}
	isDefault := map[string]bool{}
	for _, c := range l.Items {
		if _, ok := category[c.Type]; !ok {
			category[c.Type] = c.Type
			if r, err := s.Call(ctx, "PartnerCommType", "get", map[string]any{"id": c.Type}); err == nil {
				var t struct {
					Category string `json:"category_code"`
				}
				if sdk.Decode(r.Payload, &t) == nil && t.Category != "" {
					category[c.Type] = t.Category
				}
			}
		}
		cat := category[c.Type]
		all = append(all, map[string]any{"type": c.Type, "category": cat, "value": c.Value})
		if _, ok := main[cat]; !ok || (crud.AsBool(c.Default) && !isDefault[cat]) {
			main[cat], isDefault[cat] = c.Value, crud.AsBool(c.Default)
		}
		if _, ok := main[c.Type]; !ok {
			main[c.Type] = c.Value
		}
	}
	return main, all
}

func bank(ctx context.Context, s module.Services, id string) map[string]any {
	resp, err := s.Call(ctx, "PartnerBankDetail", "list", map[string]any{"query": map[string]any{"bp_id": id}})
	if err != nil {
		return nil
	}
	var l struct {
		Items []struct {
			IBAN    string `json:"iban"`
			BIC     string `json:"bic"`
			Bank    string `json:"bank_name"`
			Holder  string `json:"account_holder"`
			Default any    `json:"is_default"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil || len(l.Items) == 0 {
		return nil
	}
	b := l.Items[0]
	for _, it := range l.Items {
		if crud.AsBool(it.Default) {
			b = it
			break
		}
	}
	return map[string]any{"name": b.Bank, "iban": b.IBAN, "bic": b.BIC, "holder": b.Holder}
}

// ObjectPartner: erster wirksamer Partner eines Objekts (Mietobjekt, Gebäude,
// Wirtschaftseinheit – mit Vererbung) in der Reihenfolge der Rollen; "" = keiner.
func ObjectPartner(ctx context.Context, s module.Services, cc, object, date string, roles []string) string {
	if object == "" || len(roles) == 0 {
		return ""
	}
	resp, err := s.Call(ctx, "RealEstateSetup", "partners", map[string]any{"company": cc, "object": object, "date": date})
	if err != nil {
		return ""
	}
	var out struct {
		Partners []struct {
			Role    string `json:"role_code"`
			Partner string `json:"partner_id"`
		} `json:"partners"`
	}
	if sdk.Decode(resp.Payload, &out) != nil {
		return ""
	}
	for _, r := range roles {
		for _, p := range out.Partners {
			if strings.EqualFold(p.Role, strings.TrimSpace(r)) {
				return p.Partner
			}
		}
	}
	return ""
}

var fileNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// File: Datei für die Vorlage (Daten files: {name, content Base64}).
type File struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Signature: Unterschrift eines Geschäftspartners – das neueste abgelegte
// DMS-Dokument der Dokumentart docType zum Partner (Bild, z. B. PNG). Der
// Dateiname ist prefix + Endung; ok = false: keins vorhanden oder kein DMS.
func Signature(ctx context.Context, s module.Services, cc, partnerID, docType, prefix string) (File, bool) {
	if partnerID == "" || docType == "" {
		return File{}, false
	}
	var l struct {
		Items []struct {
			ID       string `json:"id"`
			Date     string `json:"doc_date"`
			FileName string `json:"file_name"`
		} `json:"items"`
	}
	// Partner des Dokuments oder Verknüpfung mit dem Partner
	for _, q := range []map[string]any{{"partner_id": partnerID}, {"ref_object": "BusinessPartner", "ref_key": partnerID}} {
		q["company_code"], q["doc_type"], q["status"] = cc, docType, "FILED"
		resp, err := s.Call(ctx, "DmsDocument", "list", map[string]any{"query": q, "limit": 50})
		if err == nil && sdk.Decode(resp.Payload, &l) == nil && len(l.Items) > 0 {
			break
		}
	}
	if len(l.Items) == 0 {
		return File{}, false
	}
	best := l.Items[0]
	for _, it := range l.Items {
		if it.Date > best.Date {
			best = it
		}
	}
	resp, err := s.Call(ctx, "DmsDocument", "content", map[string]any{"id": best.ID})
	if err != nil {
		return File{}, false
	}
	var c struct {
		Content  string `json:"content"`
		FileName string `json:"file_name"`
	}
	if sdk.Decode(resp.Payload, &c) != nil || c.Content == "" {
		return File{}, false
	}
	ext := strings.ToLower(path.Ext(firstNonEmpty(best.FileName, c.FileName)))
	return File{Name: fileNameRe.ReplaceAllString(prefix, "_") + ext, Content: c.Content}, true
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// Texts: am Stichtag gültige Texte eines Datensatzes je Kategorie (seit
// contract 0.39.0, z. B. Vertragstexte für Platzhalter {text:KATEGORIE}).
func Texts(ctx context.Context, s module.Services, entity, id string) map[string]any {
	return texts(ctx, s, entity, id)
}
