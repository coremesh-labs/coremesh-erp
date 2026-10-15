package dms

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Erkennung im Text (Schrittart IDENTIFY): Geschäftspartner und Vertrag aus dem
// erkannten Text – IBAN (Bankverbindungen der Partner; Bankumsätze mit der IBAN:
// zugeordneter Partner bzw. Name der Gegenseite), Vertrags-, externe
// und Kundennummer der Verträge, Telefon- und Faxnummern (Kommunikation),
// Namen und Kürzel. Jeder Fund gibt Punkte; ein Partner gilt als erkannt,
// wenn er mindestens min_score Punkte hat und klar vor dem nächsten liegt.
// Gesetzt werden Partner und Bezug zum Vertrag (nur, was noch fehlt); der
// folgende Bestätigungsschritt nimmt es bei „Nein“ zurück (Parameter undo_to).

const kIdentify = "IDENTIFY"

// Punkte je Fund.
const (
	scoreIBAN     = 5
	scorePayee    = 4 // Name der Gegenseite im Kontoauszug (IBAN aus dem Dokument)
	scoreContract = 5
	scorePhone    = 3
	scoreName     = 2
	scoreShort    = 2
	scoreName2    = 1
)

var (
	ibanRe   = regexp.MustCompile(`\b[A-Z]{2}\d{2}(?:[ ]?[A-Z0-9]{4}){3,7}(?:[ ]?[A-Z0-9]{1,3})?\b`)
	phoneRe  = regexp.MustCompile(`[+0(][\d ()/\-]{6,}\d`)
	nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
)

// foldText: klein, Umlaute ausgeschrieben, nur Buchstaben/Ziffern mit Leerzeichen.
func foldText(s string) string {
	r := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "Ä", "ae", "Ö", "oe", "Ü", "ue")
	return " " + strings.TrimSpace(nonAlnum.ReplaceAllString(r.Replace(strings.ToLower(s)), " ")) + " "
}

// compactAlnum: nur Buchstaben und Ziffern, groß (Nummern mit und ohne Trennzeichen gleich).
func compactAlnum(s string) string {
	return strings.ToUpper(strings.ReplaceAll(foldText(s), " ", ""))
}

// phoneDigits: Ziffern einer Telefonnummer, +49 bzw. 0049 als 0.
func phoneDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if strings.HasPrefix(strings.TrimSpace(s), "+") {
		d = strings.TrimPrefix(d, "49")
		return "0" + d
	}
	if strings.HasPrefix(d, "0049") {
		return "0" + strings.TrimPrefix(d, "0049")
	}
	return d
}

// identification ist das Ergebnis der Erkennung.
type identification struct {
	Partner, Contract string
	Reasons           []string
}

// identify sucht Partner und Vertrag im Text des Dokuments.
func (m *Module) identify(ctx context.Context, d *docRow, minScore int) (*identification, error) {
	text := m.docText(ctx, d.ID)
	if strings.TrimSpace(text) == "" {
		return nil, crud.Invalid("kein erkannter Text – vorher Texterkennung")
	}
	folded, compact := foldText(text), compactAlnum(text)
	scores := map[string]int{}
	reasons := map[string][]string{}
	hit := func(bp string, pts int, why string) {
		if bp == "" {
			return
		}
		scores[bp] += pts
		reasons[bp] = append(reasons[bp], why)
	}
	list := func(object string, query map[string]any) []map[string]any {
		resp, err := m.services.Call(ctx, object, "list", map[string]any{"query": query})
		if err != nil {
			return nil
		}
		var l struct {
			Items []map[string]any `json:"items"`
		}
		if sdk.Decode(resp.Payload, &l) != nil {
			return nil
		}
		return l.Items
	}
	// IBAN
	ibans := map[string]bool{}
	for _, f := range ibanRe.FindAllString(strings.ToUpper(text), -1) {
		if i := strings.ReplaceAll(f, " ", ""); ibanValid(i) {
			ibans[i] = true
		}
	}
	if len(ibans) > 0 {
		for _, b := range list("PartnerBankDetail", map[string]any{}) {
			if i := strings.ReplaceAll(strings.ToUpper(crud.Str(b["iban"])), " ", ""); ibans[i] {
				hit(crud.Str(b["bp_id"]), scoreIBAN, "IBAN")
			}
		}
	}
	// Kontoauszug: Umsätze mit einer IBAN des Dokuments – der dort zugeordnete
	// Partner, sonst der Name der Gegenseite (unten mit den Partnernamen verglichen).
	var payees []string
	own := m.ownIBANs(ctx)
	for i := range ibans {
		if own[i] {
			continue
		}
		p := m.payeeOf(ctx, d.CC, i)
		if p.Partner != "" {
			hit(p.Partner, scoreIBAN, "IBAN laut Kontoauszug")
		} else if strings.TrimSpace(p.Name) != "" {
			payees = append(payees, foldText(p.Name))
		}
	}
	// Telefon, Fax
	var phones []string
	for _, f := range phoneRe.FindAllString(text, -1) {
		if p := phoneDigits(f); len(p) >= 7 {
			phones = append(phones, p)
		}
	}
	if len(phones) > 0 {
		for _, c := range list("PartnerContact", map[string]any{}) {
			v := crud.Str(c["value"])
			if strings.Contains(v, "@") {
				continue
			}
			p := phoneDigits(v)
			if len(p) < 7 {
				continue
			}
			for _, t := range phones {
				if t == p || strings.HasSuffix(t, p) || strings.HasSuffix(p, t) && len(t) >= 9 {
					hit(crud.Str(c["bp_id"]), scorePhone, "Telefon")
					break
				}
			}
		}
	}
	// Verträge: Vertrags-, externe und Kundennummer
	type cand struct{ id, partner, why string }
	var contracts []cand
	for _, c := range list("Contract", map[string]any{"company_code": d.CC}) {
		for _, f := range [][2]string{{"customer_number", "Kundennummer"}, {"external_number", "Vertragsnummer"}, {"contract_id", "Vertragsnummer"}} {
			n := compactAlnum(crud.Str(c[f[0]]))
			if len(n) >= 5 && strings.Contains(compact, n) {
				contracts = append(contracts, cand{crud.Str(c["id"]), crud.Str(c["partner_id"]), f[1]})
				hit(crud.Str(c["partner_id"]), scoreContract, f[1]+" "+shortKey(crud.Str(c["id"]), d.CC))
				break
			}
		}
	}
	// Namen, Kürzel
	for _, p := range list("BusinessPartner", map[string]any{}) {
		id := crud.Str(p["id"])
		n1, n2 := foldText(crud.Str(p["name1"])), foldText(crud.Str(p["name2"]))
		if len(strings.TrimSpace(n1)) >= 4 && strings.Contains(folded, n1) {
			hit(id, scoreName, "Name")
			if len(strings.TrimSpace(n2)) >= 3 && strings.Contains(folded, n2) {
				hit(id, scoreName2, "Name 2")
			}
		}
		for _, py := range payees {
			if len(strings.TrimSpace(n1)) >= 4 && (strings.Contains(py, n1) || strings.Contains(n1, py)) {
				hit(id, scorePayee, "Name laut Kontoauszug")
				break
			}
		}
		if a := foldText(crud.Str(p["abbreviation"])); len(strings.TrimSpace(a)) >= 3 && strings.Contains(folded, a) {
			hit(id, scoreShort, "Kürzel")
		}
	}
	out := &identification{}
	type ps struct {
		id    string
		score int
	}
	var ranked []ps
	for id, s := range scores {
		ranked = append(ranked, ps{id, s})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].id < ranked[j].id
	})
	if len(ranked) > 0 && ranked[0].score >= minScore && (len(ranked) == 1 || ranked[0].score-ranked[1].score >= 2) {
		out.Partner = ranked[0].id
		out.Reasons = reasons[out.Partner]
	}
	// Vertrag: eindeutig, bei erkanntem Partner einer von ihm.
	var cs []cand
	for _, c := range contracts {
		if out.Partner == "" || c.partner == out.Partner {
			cs = append(cs, c)
		}
	}
	if len(cs) == 1 {
		out.Contract = cs[0].id
	}
	return out, nil
}

// identifyStep: Programmschritt – setzt, was fehlt; Ergebnis für den Verlauf.
func (m *Module) identifyStep(ctx context.Context, d *docRow, s *stepDef) (string, error) {
	min := scoreName + scoreName2
	if v, err := strconv.Atoi(s.param(ctx, "min_score")); err == nil && v > 0 {
		min = v
	}
	id, err := m.identify(ctx, d, min)
	if err != nil {
		return "", err
	}
	var notes []string
	if id.Partner != "" && d.Partner == "" {
		if _, err := m.db.Exec(ctx, `UPDATE dms__document SET partner_id = ? WHERE id = ?`, id.Partner, d.ID); err != nil {
			return "", err
		}
		notes = append(notes, fmt.Sprintf("Partner %s (%s)", m.partnerName(ctx, id.Partner), strings.Join(id.Reasons, ", ")))
	}
	if id.Contract != "" {
		if err := m.insertRef(ctx, d, "Contract", id.Contract); err != nil {
			return "", err
		}
		notes = append(notes, "Vertrag "+shortKey(id.Contract, d.CC))
	}
	if len(notes) == 0 {
		return "", crud.Invalid("nichts eindeutig erkannt")
	}
	return strings.Join(notes, ", "), nil
}

// identified: Partner oder Bezug vorhanden (Bedingung when = identified).
func (m *Module) identified(ctx context.Context, d *docRow) bool {
	if d.Partner != "" {
		return true
	}
	refs, _ := m.activeRefs(ctx, d.ID)
	return len(refs) > 0
}

// refsText: Bezüge für Fragen ({refs}).
func (m *Module) refsText(ctx context.Context, d *docRow) string {
	refs, _ := m.activeRefs(ctx, d.ID)
	var out []string
	for _, r := range refs {
		out = append(out, r.Object+" "+shortKey(r.Key, d.CC))
	}
	if len(out) == 0 {
		return "–"
	}
	return strings.Join(out, ", ")
}

// undoTo: Stand vor dem Schritt step wiederherstellen (Bestätigung „Nein“, Parameter undo_to).
func (m *Module) undoTo(ctx context.Context, d *docRow, step string) error {
	t := m.trail(ctx, d.ID)
	for i := len(t) - 1; i > 0; i-- {
		if t[i].Step == step {
			if err := m.restoreState(ctx, d.ID, t[i-1]); err != nil {
				return err
			}
			m.appendLog(ctx, d.ID, "Erkennung zurückgenommen")
			return nil
		}
	}
	return nil
}

// ibanValid: Prüfziffer nach ISO 13616 (mod 97 = 1).
func ibanValid(iban string) bool {
	if len(iban) < 15 || len(iban) > 34 {
		return false
	}
	rem := 0
	for _, r := range iban[4:] + iban[:4] {
		switch {
		case r >= '0' && r <= '9':
			rem = (rem*10 + int(r-'0')) % 97
		case r >= 'A' && r <= 'Z':
			v := int(r-'A') + 10
			rem = (rem*100 + v) % 97
		default:
			return false
		}
	}
	return rem == 1
}
