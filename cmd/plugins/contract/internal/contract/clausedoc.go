package contract

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/tagservice"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
)

// Vertragsdokument (seit 0.39.0): Bedingungen der Klauseln und Platzhalter mit den
// Daten des Vertrags am Stichtag (Stichtag, frühestens Vertragsbeginn), Ausgabe über
// eine Vorlage der Verwendung CONTRACT_DOCUMENT (Datenformat contract.document/1).
//
//	{vertrag:feld}     Feld des Vertrags (Datum TT.MM.JJJJ), dazu type_name, number
//	{objekt:feld}      Feld des Hauptobjekts
//	{kondition:KM+NK}  Summe der Konditionen am Stichtag (deutsch, mit Nachkommastellen der Währung)
//	{tag:CODE}         Tag am Vertrag, {objekttag:CODE} am Hauptobjekt
//	{partei:feld}      unsere Partei: name, street, zip, city, iban, bic, bank
//	{text:KATEGORIE}   Vertragstext
//
// Unbekannte Quellen und fehlende Werte sind ein Fehler – das Dokument bleibt sonst
// unvollständig, ohne dass es auffällt.

const usageContractDocument = "CONTRACT_DOCUMENT"

var placeholderRe = regexp.MustCompile(`\{([a-z]+):([^{}]+)\}`)

// clauseData: Daten eines Vertrags für Bedingungen und Platzhalter (bei Bedarf geladen).
type clauseData struct {
	m        *Module
	c        *contractRow
	set      *clauseSetRow
	date     string
	rec      crud.Record
	objType  string
	objID    string
	obj      map[string]any
	partners []string
	tags     map[string]map[string]tagShown
	texts    map[string]any
	pos      []position
	dec      int
	posDone  bool
	own      map[string]any
}

// tagShown: Wert eines Tags zum Vergleichen (Code bzw. Wert) und zum Drucken.
type tagShown struct{ raw, shown string }

func (m *Module) newClauseData(ctx context.Context, c *contractRow, set *clauseSetRow) (*clauseData, error) {
	d := &clauseData{m: m, c: c, set: set, date: max(crud.KeyDate(ctx), c.ValidFrom), tags: map[string]map[string]tagShown{}}
	if c.ValidTo != "" && d.date > c.ValidTo {
		d.date = c.ValidTo
	}
	rec, err := m.set.Entity("Contract").Load(ctx, crud.Record{"company_code": c.CompanyCode, "contract_id": c.ID})
	if err != nil {
		return nil, err
	}
	d.rec = rec
	res, err := m.db.Query(ctx, `SELECT object_type, object_id FROM contract__object WHERE company_code = ? AND contract_id = ?
		AND valid_from <= ? AND valid_to >= ? ORDER BY is_main DESC, valid_from`, c.CompanyCode, c.ID, d.date, d.date)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) > 0 {
		d.objType, d.objID = crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1])
	}
	ct, err := m.contractTypeOf(ctx, c.CompanyCode, c.Type)
	if err != nil {
		return nil, err
	}
	if c.Partner != "" {
		d.partners = append(d.partners, c.Partner)
	}
	res, err = m.db.Query(ctx, `SELECT partner_id FROM contract__partner WHERE company_code = ? AND contract_id = ? AND role_code = ?
		AND valid_from <= ? AND valid_to >= ? ORDER BY valid_from, partner_id`, c.CompanyCode, c.ID, ct.MainRole, d.date, d.date)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Rows {
		if p := crud.Str(r[0]); !slices.Contains(d.partners, p) {
			d.partners = append(d.partners, p)
		}
	}
	return d, nil
}

// object: Hauptobjekt (nil = keins am Stichtag).
func (d *clauseData) object(ctx context.Context) map[string]any {
	if d.obj == nil && d.objID != "" {
		if resp, err := d.m.services.Call(ctx, d.objType, "get", map[string]any{"id": d.c.CompanyCode + "|" + d.objID}); err == nil {
			_ = sdk.Decode(resp.Payload, &d.obj)
		}
	}
	return d.obj
}

// tagsOf: Tags eines Datensatzes am Stichtag.
func (d *clauseData) tagsOf(ctx context.Context, entity, id string) (map[string]tagShown, error) {
	key := entity + "/" + id
	if t, ok := d.tags[key]; ok {
		return t, nil
	}
	et, err := tagservice.New(d.m.services).Get(ctx, tagservice.GetRequest{EntityType: entity, EntityID: id, CompanyCode: d.c.CompanyCode, EffectiveDate: d.date})
	if err != nil {
		return nil, fmt.Errorf("Tags %s %s: %w", entity, id, err)
	}
	out := map[string]tagShown{}
	for _, a := range et.Values {
		out[a.Tag] = shownTag(a)
	}
	d.tags[key] = out
	return out, nil
}

func shownTag(a tagservice.Assignment) tagShown {
	v := a.Value
	switch {
	case v.Option != nil:
		return tagShown{*v.Option, firstNonEmpty(a.OptionLabel, *v.Option)}
	case v.String != nil:
		return tagShown{*v.String, *v.String}
	case v.Integer != nil:
		s := fmt.Sprint(*v.Integer)
		return tagShown{s, s}
	case v.Date != nil:
		return tagShown{*v.Date, germanDate(*v.Date)}
	case v.Amount != nil:
		s := pricing.FormatDE(*v.Amount)
		if v.Currency != nil {
			s += " " + *v.Currency
		}
		return tagShown{*v.Amount, s}
	case v.Timestamp != nil:
		return tagShown{*v.Timestamp, *v.Timestamp}
	case v.Ref != nil:
		return tagShown{*v.Ref, firstNonEmpty(a.RefLabel, *v.Ref)}
	}
	return tagShown{}
}

// text: Vertragstext einer Kategorie ("" = keiner).
func (d *clauseData) text(ctx context.Context, cat string) string {
	if d.texts == nil {
		d.texts = reportapi.Texts(ctx, d.m.services, "Contract", d.c.CompanyCode+"|"+d.c.ID)
	}
	return strings.TrimSpace(crud.Str(d.texts[strings.ToUpper(cat)]))
}

// field: Feld des Vertrags; unbefristet ist leer. ok = false: Feld gibt es nicht.
func (d *clauseData) field(name string) (string, bool) {
	v, ok := d.rec[name]
	if !ok {
		return "", false
	}
	s := strings.TrimSpace(crud.Str(v))
	if s >= crud.DateMax && strings.HasPrefix(s, "9999-12-31") {
		s = ""
	}
	return s, true
}

// conditions: Konditionen am Stichtag (Betrag in der kleinsten Einheit).
func (d *clauseData) conditions(ctx context.Context) ([]position, error) {
	if !d.posDone {
		ps, _, dec, err := d.m.positions(ctx, d.c, d.date)
		if err != nil {
			return nil, err
		}
		d.pos, d.dec, d.posDone = ps, dec, true
	}
	return d.pos, nil
}

// holds: Bedingung der Klausel erfüllt? why: Grund, wenn nicht.
func (d *clauseData) holds(ctx context.Context, c clauseRow) (bool, string, error) {
	if c.Source == "" {
		return true, "", nil
	}
	var vals []string // vorhandene Werte (bei Tags an Partnern je Partner einer)
	switch c.Source {
	case condContractTag:
		t, err := d.tagsOf(ctx, "Contract", d.c.CompanyCode+"|"+d.c.ID)
		if err != nil {
			return false, "", err
		}
		vals = append(vals, t[c.Name].raw)
	case condObjectTag:
		if d.objID != "" {
			t, err := d.tagsOf(ctx, d.objType, d.c.CompanyCode+"|"+d.objID)
			if err != nil {
				return false, "", err
			}
			vals = append(vals, t[c.Name].raw)
		}
	case condPartnerTag:
		for _, p := range d.partners {
			t, err := d.tagsOf(ctx, "BusinessPartner", p)
			if err != nil {
				return false, "", err
			}
			vals = append(vals, t[c.Name].raw)
		}
	case condField:
		v, ok := d.field(c.Name)
		if !ok {
			return false, "", crud.Invalid("Feld %s gibt es am Vertrag nicht", c.Name)
		}
		vals = append(vals, v)
	case condCondition:
		ps, err := d.conditions(ctx)
		if err != nil {
			return false, "", err
		}
		for _, p := range ps {
			if strings.EqualFold(p.ConditionType, c.Name) {
				vals = append(vals, p.ConditionType)
			}
		}
	case condText:
		vals = append(vals, d.text(ctx, c.Name))
	default:
		return false, "", crud.Invalid("Quelle der Bedingung %q unbekannt", c.Source)
	}
	want := splitList(c.Values)
	ok := slices.ContainsFunc(vals, func(v string) bool {
		if strings.TrimSpace(v) == "" {
			return false
		}
		return len(want) == 0 || slices.ContainsFunc(want, func(w string) bool { return strings.EqualFold(w, strings.TrimSpace(v)) })
	})
	if c.Negate {
		ok = !ok
	}
	if ok {
		return true, "", nil
	}
	return false, "Bedingung „" + conditionText(crud.Record{"cond_source": c.Source, "cond_name": c.Name, "cond_values": c.Values, "cond_negate": c.Negate}) + "“ nicht erfüllt", nil
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ownParty: unsere Partei – Partner am Objekt in den Rollen des Sets, sonst der des Buchungskreises.
func (d *clauseData) ownParty(ctx context.Context) map[string]any {
	if d.own != nil {
		return d.own
	}
	id := ""
	if roles := splitList(d.set.OwnRoles); len(roles) > 0 && d.objID != "" {
		id = reportapi.ObjectPartner(ctx, d.m.services, d.c.CompanyCode, d.objID, d.date, roles)
	}
	if id == "" {
		if p, ok := reportapi.Company(ctx, d.m.services, d.c.CompanyCode)["partner"].(map[string]any); ok {
			id = crud.Str(p["id"])
		}
	}
	d.own = map[string]any{"label": d.set.OwnLabel}
	if p := reportapi.Partner(ctx, d.m.services, id); p != nil {
		for k, v := range p {
			d.own[k] = v
		}
	}
	return d.own
}

// fill: Platzhalter eines Textes einsetzen; fehlende Werte sammelt missing.
func (d *clauseData) fill(ctx context.Context, s string, missing *[]string) string {
	return placeholderRe.ReplaceAllStringFunc(s, func(t string) string {
		p := placeholderRe.FindStringSubmatch(t)
		v, err := d.value(ctx, p[1], strings.TrimSpace(p[2]))
		if err != nil {
			*missing = append(*missing, t+": "+err.Error())
			return t
		}
		return v
	})
}

func (d *clauseData) value(ctx context.Context, src, name string) (string, error) {
	shown := func(v any) string {
		s := strings.TrimSpace(crud.Str(v))
		if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
			if strings.HasPrefix(s, "9999-12-31") {
				return "unbefristet"
			}
			return germanDate(s[:10])
		}
		return s
	}
	switch src {
	case "vertrag":
		switch name {
		case "type_name":
			ct, err := d.m.contractTypeOf(ctx, d.c.CompanyCode, d.c.Type)
			if err != nil {
				return "", err
			}
			return ct.Name, nil
		case "number":
			return firstNonEmpty(crud.Str(d.rec["external_number"]), d.c.ID), nil
		}
		v, ok := d.rec[name]
		if !ok {
			return "", fmt.Errorf("Feld gibt es am Vertrag nicht")
		}
		return shown(v), nil
	case "objekt":
		o := d.object(ctx)
		if o == nil {
			return "", fmt.Errorf("kein Objekt am %s", germanDate(d.date))
		}
		v, ok := o[name]
		if !ok {
			return "", fmt.Errorf("Feld gibt es am Objekt %s nicht", d.objID)
		}
		return shown(v), nil
	case "kondition":
		ps, err := d.conditions(ctx)
		if err != nil {
			return "", err
		}
		var sum int64
		for _, k := range strings.Split(name, "+") {
			k = strings.TrimSpace(k)
			found := false
			for _, p := range ps {
				if strings.EqualFold(p.ConditionType, k) {
					sum, found = sum+p.Amount, true
				}
			}
			if !found {
				return "", fmt.Errorf("Kondition %s fehlt am %s", k, germanDate(d.date))
			}
		}
		return pricing.FormatDE(new(big.Rat).SetFrac(big.NewInt(sum), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d.dec)), nil)).FloatString(d.dec)), nil
	case "tag", "objekttag":
		entity, id := "Contract", d.c.CompanyCode+"|"+d.c.ID
		if src == "objekttag" {
			if d.objID == "" {
				return "", fmt.Errorf("kein Objekt am %s", germanDate(d.date))
			}
			entity, id = d.objType, d.c.CompanyCode+"|"+d.objID
		}
		t, err := d.tagsOf(ctx, entity, id)
		if err != nil {
			return "", err
		}
		v, ok := t[strings.ToUpper(name)]
		if !ok || v.shown == "" {
			return "", fmt.Errorf("Tag fehlt")
		}
		return v.shown, nil
	case "partei":
		own := d.ownParty(ctx)
		var v any
		switch name {
		case "iban":
			b, _ := own["bank"].(map[string]any)
			v = groupIBAN(crud.Str(b["iban"]))
		case "bic":
			b, _ := own["bank"].(map[string]any)
			v = b[name]
		case "bank":
			b, _ := own["bank"].(map[string]any)
			v = b["name"]
		default:
			v = own[name]
		}
		if s := shown(v); s != "" {
			return s, nil
		}
		return "", fmt.Errorf("fehlt bei unserer Partei")
	case "text":
		return d.text(ctx, name), nil
	}
	return "", fmt.Errorf("Quelle unbekannt (vertrag, objekt, kondition, tag, objekttag, partei, text)")
}

// partyItems: Vertragspartner für die Vorlage (Name, Anschrift, Tags).
func (d *clauseData) partyItems(ctx context.Context) []any {
	var out []any
	for _, id := range d.partners {
		p := reportapi.Partner(ctx, d.m.services, id)
		if p == nil {
			p = map[string]any{"id": id, "name": id}
		}
		tags := map[string]any{}
		if t, err := d.tagsOf(ctx, "BusinessPartner", id); err == nil {
			for k, v := range t {
				tags[k] = v.raw
			}
		}
		p["tags"] = tags
		out = append(out, p)
	}
	return out
}

// --- Aktion „Vertragsdokument (PDF) …“ --------------------------------------------------

func (m *Module) clauseDocumentAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in clauseActionInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	c, err := m.contractOfID(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if ok, err := sdk.CheckAccess(ctx, "Contract", "get", c.CompanyCode); err != nil || !ok {
		return sdk.Response{}, fmt.Errorf("%w: Vertrag %s lesen", sdk.ErrPermissionDenied, c.ID)
	}
	setCode, at := m.copyState(ctx, c)
	if setCode == "" {
		return sdk.Response{}, crud.Invalid("Vertrag %s hat keine Vertragsklauseln – zuerst „Vertragsklauseln erstellen …“", c.ID)
	}
	set, err := m.clauseSetOf(ctx, c.CompanyCode, setCode)
	if err != nil {
		return sdk.Response{}, err
	}
	data, err := m.clauseDocumentData(ctx, c, set, at)
	if err != nil {
		return sdk.Response{}, err
	}
	template := firstNonEmpty(strings.TrimSpace(in.Data.Template), set.Template)
	if template != "" && !strings.Contains(template, "|") {
		template += "|de"
	}
	out, err := reportapi.Render(ctx, m.services, c.CompanyCode, template, "Vertragsdokument "+c.ID, data)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: out}, nil
}

// clauseDocumentData: Daten im Format contract.document/1.
func (m *Module) clauseDocumentData(ctx context.Context, c *contractRow, set *clauseSetRow, at string) (map[string]any, error) {
	d, err := m.newClauseData(ctx, c, set)
	if err != nil {
		return nil, err
	}
	ct, err := m.contractTypeOf(ctx, c.CompanyCode, c.Type)
	if err != nil {
		return nil, err
	}
	res, err := m.db.Query(ctx, `SELECT paragraph_no, subsection_no, title, text FROM contract__clause_copy
		WHERE company_code = ? AND contract_id = ? ORDER BY seq`, c.CompanyCode, c.ID)
	if err != nil {
		return nil, err
	}
	var missing []string
	var paras []any
	var cur map[string]any
	var items []any
	var lastItem map[string]any
	flush := func() {
		if cur != nil {
			if len(items) > 0 {
				cur["items"] = items
			}
			paras = append(paras, cur)
		}
	}
	for _, r := range res.Rows {
		p, s, title := toInt(r[0]), toInt(r[1]), crud.Str(r[2])
		text := strings.TrimSpace(d.fill(ctx, crud.Str(r[3]), &missing))
		if cur == nil || crud.Str(cur["no"]) != fmt.Sprint(p) {
			flush()
			cur, items, lastItem = map[string]any{"no": fmt.Sprint(p), "title": title}, nil, nil
		}
		if crud.Str(cur["title"]) == "" {
			cur["title"] = title
		}
		if text == "" {
			continue
		}
		if s == 0 {
			cur["text"] = strings.TrimSpace(crud.Str(cur["text"]) + "\n\n" + text)
			continue
		}
		if lastItem != nil && lastItem["no"] == fmt.Sprint(s) {
			lastItem["text"] = crud.Str(lastItem["text"]) + "\n\n" + text
			continue
		}
		lastItem = map[string]any{"no": fmt.Sprint(s), "text": text}
		items = append(items, lastItem)
	}
	flush()
	if len(missing) > 0 {
		return nil, crud.Invalid("Platzhalter ohne Wert: %s", strings.Join(missing, "; "))
	}
	if len(paras) == 0 {
		return nil, crud.Invalid("Vertrag %s: keine Klauseln mit Text", c.ID)
	}
	version := crud.KeyDate(ctx)
	if len(at) >= 10 {
		version = at[:10]
	}
	contract := map[string]any{"id": c.ID, "number": firstNonEmpty(crud.Str(d.rec["external_number"]), c.ID), "type": c.Type, "type_name": ct.Name,
		"designation": c.Designation, "valid_from": dateOf(c.ValidFrom)}
	if to := dateOf(c.ValidTo); to != "" && to < "9999-12-31" {
		contract["valid_to"] = to
	}
	return map[string]any{"format": "contract.document/1", "title": firstNonEmpty(set.Title, ct.Name), "version": version, "contract": contract,
		"company": reportapi.Company(ctx, m.services, c.CompanyCode), "own": d.ownParty(ctx),
		"partners": map[string]any{"label": set.PartnerLabel, "items": d.partyItems(ctx)}, "paragraphs": paras}, nil
}

// groupIBAN: IBAN in Vierergruppen (DE89 3704 0044 …).
func groupIBAN(s string) string {
	s = strings.ReplaceAll(s, " ", "")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}
