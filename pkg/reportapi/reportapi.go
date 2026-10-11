// Package reportapi: gemeinsame Bausteine für Berichte der Fachmodule, die
// über das Vorlagen-Modul (template) ausgegeben werden – Kopfdaten des
// Buchungskreises mit seinem Geschäftspartner, Auswahl der Vorlagen einer
// Verwendung und der Aufruf Template.render/get mit dem Ergebnis für die
// Oberfläche (Meldung, Datei zum Speichern).
package reportapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Wait: Sekunden, die ein Bericht auf das Ergebnis der Vorlage wartet.
const Wait = 180

// DisplayName: Name eines Geschäftspartners zum Anzeigen (Personen: Vorname Name); "" = nicht lesbar.
func DisplayName(ctx context.Context, s module.Services, id string) string {
	if id == "" {
		return ""
	}
	resp, err := s.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return ""
	}
	var bp struct {
		Type  string `json:"type"`
		Name1 string `json:"name1"`
		Name2 string `json:"name2"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return ""
	}
	if bp.Type == "PERSON" {
		return strings.TrimSpace(bp.Name2 + " " + bp.Name1)
	}
	return strings.TrimSpace(bp.Name1 + " " + bp.Name2)
}

// Company: Buchungskreis mit seinem Geschäftspartner und dessen Anschrift
// (Hauptanschrift, sonst die erste) – {code, name, partner: {id, name, street, zip, city, country}}.
func Company(ctx context.Context, s module.Services, cc string) map[string]any {
	out := map[string]any{"code": cc, "name": "Buchungskreis " + cc}
	resp, err := s.Call(ctx, "CompanyCode", "get", map[string]any{"id": cc})
	if err != nil {
		return out
	}
	var c struct {
		Description string `json:"description"`
		PartnerID   string `json:"partner_id"`
	}
	if sdk.Decode(resp.Payload, &c) != nil {
		return out
	}
	if c.Description != "" {
		out["name"] = c.Description
	}
	if c.PartnerID == "" {
		return out
	}
	p := map[string]any{"id": c.PartnerID, "name": DisplayName(ctx, s, c.PartnerID)}
	out["partner"] = p
	resp, err = s.Call(ctx, "PartnerAddress", "list", map[string]any{"query": map[string]any{"bp_id": c.PartnerID}})
	if err != nil {
		return out
	}
	var l struct {
		Items []struct {
			AddressID string `json:"address_id"`
			Role      string `json:"address_role_code"`
			Default   any    `json:"is_default"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil || len(l.Items) == 0 {
		return out
	}
	best := l.Items[0]
	for _, it := range l.Items {
		if it.Role == "MAIN" && (crud.AsBool(it.Default) || best.Role != "MAIN") {
			best = it
		}
	}
	resp, err = s.Call(ctx, "PartnerAddressData", "get", map[string]any{"id": best.AddressID})
	if err != nil {
		return out
	}
	var a struct {
		Street  string `json:"street"`
		HouseNo string `json:"house_no"`
		Zip     string `json:"zip_code"`
		City    string `json:"city"`
		Country string `json:"country"`
	}
	if sdk.Decode(resp.Payload, &a) == nil {
		p["street"] = strings.TrimSpace(a.Street + " " + a.HouseNo)
		p["zip"], p["city"], p["country"] = a.Zip, a.City, a.Country
	}
	return out
}

// Templates: aktive Vorlagen einer Verwendung als Auswahl (Wert Code|Sprache).
func Templates(ctx context.Context, s module.Services, usage string) []metamodel.Option {
	resp, err := s.Call(ctx, "TemplateDefinition", "list", map[string]any{"query": map[string]any{"usage": usage, "is_active": "true"}})
	if err != nil {
		return nil
	}
	var l struct {
		Items []struct {
			Code     string `json:"code"`
			Language string `json:"language"`
			Name     string `json:"name"`
			Target   string `json:"target"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return nil
	}
	var out []metamodel.Option
	for _, it := range l.Items {
		out = append(out, metamodel.Option{Value: it.Code + "|" + it.Language, Label: fmt.Sprintf("%s (%s, %s)", it.Name, it.Target, it.Language)})
	}
	return out
}

// Render gibt die Daten mit der Vorlage (Code|Sprache) aus, wartet auf das
// Ergebnis und liefert die Felder für die Oberfläche: message (mit head),
// download (Datei), job_number. Ein Fehler der Vorlage ist ein Fehler.
func Render(ctx context.Context, s module.Services, cc, template, head string, data map[string]any) (map[string]any, error) {
	code, lang, ok := strings.Cut(strings.TrimSpace(template), "|")
	if !ok || code == "" {
		return nil, crud.Invalid("Vorlage fehlt")
	}
	resp, err := s.Call(ctx, "Template", "render", map[string]any{"template": code, "language": lang, "company_code": cc, "data": data, "wait": Wait})
	if err != nil {
		return nil, fmt.Errorf("Vorlagen: %w", err)
	}
	var job struct {
		JobNumber string `json:"job_number"`
	}
	if err := sdk.Decode(resp.Payload, &job); err != nil {
		return nil, err
	}
	resp, err = s.Call(ctx, "Template", "get", map[string]any{"job_number": job.JobNumber})
	if err != nil {
		return nil, fmt.Errorf("Vorlagen: %w", err)
	}
	var res struct {
		Status      string `json:"status"`
		StatusText  string `json:"status_text"`
		ContentType string `json:"content_type"`
		FileName    string `json:"file_name"`
		Content     string `json:"content"`
	}
	if err := sdk.Decode(resp.Payload, &res); err != nil {
		return nil, err
	}
	out := map[string]any{"job_number": job.JobNumber}
	head += " – Auftrag " + job.JobNumber
	switch res.Status {
	case "DONE":
		out["message"] = head + ": " + res.FileName
		if _, err := base64.StdEncoding.DecodeString(res.Content); err == nil {
			out["download"] = metamodel.Content{Content: res.Content, ContentType: res.ContentType, FileName: res.FileName}
		}
	case "ERROR":
		return nil, crud.Invalid("Auftrag %s: %s (Anhänge unter Vorlagen → Aufträge)", job.JobNumber, res.StatusText)
	default:
		out["message"] = head + " läuft noch – das Ergebnis steht dann unter Vorlagen → Aufträge"
	}
	return out, nil
}

// MoneyDE: Betrag deutsch mit zwei Stellen (1.234,56).
func MoneyDE(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%.2f", v)
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	out := b.String() + "," + frac
	if neg && out != "0,00" {
		out = "-" + out
	}
	return out
}
