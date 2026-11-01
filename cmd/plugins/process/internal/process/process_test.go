package process

import (
	"context"
	"testing"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Testbestand: erfundene Hausverwaltung mit Namen aus Goethes Faust.

func (e *env) at(day string) {
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		e.t.Fatal(err)
	}
	e.m.now = func() time.Time { return t.Add(9 * time.Hour) }
}

func (e *env) as(user string) func() {
	old := e.ctx
	e.ctx = sdk.WithCall(e.ctx, sdk.CallContext{RequestID: "test", UserID: user})
	return func() { e.ctx = old }
}

func (e *env) steps(id string) []map[string]any {
	e.t.Helper()
	return items(e.must(stepObject, "list", map[string]any{"query": map[string]any{"company_code": "1000", "process_id": id}}))
}

func (e *env) get(id string) map[string]any {
	e.t.Helper()
	return e.must(processObject, "get", map[string]any{"id": "1000|" + id})
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.h.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// TestDamageProcess: Schadensmeldung mit Schritten und Fristen aus dem Katalog,
// Pflichtfelder solange offen, „wartet auf“, Schritt-Fristen ab dem vorigen
// Schritt, Abschluss nur ohne offene Pflichtschritte und mit Erledigungsart.
func TestDamageProcess(t *testing.T) {
	e := setup(t)
	e.at("2026-03-02")
	e.h.partners["BP-GRETCHEN"] = "Gretchen"
	e.h.refs["RentObject|1000|AK-01"] = true
	p := e.create(processObject, map[string]any{"company_code": "1000", "process_type": "schaden", "subject": "Wasserfleck an der Decke",
		"partner_id": "BP-GRETCHEN", "object_id": "AK-01", "priority": "high"})
	if p["process_id"] != "SM-2026-0001" || p["status"] != "NEW" || p["assignee"] != "mephisto" || p["perspective"] != "VERWALTUNG" {
		t.Fatalf("Vorgang: %v", p)
	}
	got := e.get("SM-2026-0001")
	if got["follow_up"] != "2026-03-05" || got["ack_due"] != "2026-03-04" || got["answer_due"] != "2026-03-16" ||
		got["next_due"] != "2026-03-03" || got["next_step"] != "Schaden aufnehmen" {
		t.Fatalf("Fristen: %v", got)
	}
	if labels, _ := got["_labels"].(map[string]any); labels["partner_id"] != "Gretchen" || labels["status"] != "neu" {
		t.Fatalf("Bezeichnungen: %v", got["_labels"])
	}
	st := e.steps("SM-2026-0001")
	if len(st) != 6 || st[0]["due_date"] != "2026-03-03" || st[2]["due_date"] != nil || st[5]["required"] != false {
		t.Fatalf("Schritte: %v", st)
	}
	// Bezug unbekannt, Status unbekannt
	_, err := e.call(processObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "process_type": "SCHADEN", "subject": "x", "contract_id": "MV-0815"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannter Vertrag")
	_, err = e.call(processObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "process_type": "SCHADEN", "subject": "x", "status": "KAPUTT"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannter Status")

	upd := func(data map[string]any) error {
		_, err := e.call(processObject, "update", map[string]any{"id": "1000|SM-2026-0001", "data": data})
		return err
	}
	expect(t, upd(map[string]any{"follow_up": ""}), sdk.ErrInvalidArgument, "Wiedervorlage Pflicht")
	expect(t, upd(map[string]any{"status": "WAIT_CRAFT"}), sdk.ErrInvalidArgument, "wartet ohne „wartet auf“")
	if err := upd(map[string]any{"status": "WAIT_CRAFT", "waiting_for": "Dachdecker Valentin, Angebot", "follow_up": "2026-03-10"}); err != nil {
		t.Fatal(err)
	}
	expect(t, upd(map[string]any{"status": "DONE"}), sdk.ErrInvalidArgument, "Abschluss ohne Erledigungsart")
	expect(t, upd(map[string]any{"status": "DONE", "resolution": "SOLVED"}), sdk.ErrInvalidArgument, "offene Pflichtschritte")

	// Schritte: 20 erledigt → 30 (ab vorigem Schritt) bekommt seine Frist.
	e.at("2026-03-04")
	e.must(stepObject, "done", map[string]any{"id": "1000|SM-2026-0001|10", "data": map[string]any{"step_note": "Fotos im DMS"}})
	e.must(stepObject, "done", map[string]any{"id": "1000|SM-2026-0001|20"})
	if _, err := e.call(stepObject, "done", map[string]any{"id": "1000|SM-2026-0001|20"}); err == nil {
		t.Fatal("Schritt doppelt erledigt")
	}
	st = e.steps("SM-2026-0001")
	if st[2]["due_date"] != "2026-03-07" || st[0]["note"] != "Fotos im DMS" || st[0]["done_by"] != "mephisto" {
		t.Fatalf("Schritt 30: %v / %v", st[2], st[0])
	}
	// Die Eingangsbestätigung (bis 4. März) ist noch offen und damit die nächste Frist.
	if got = e.get("SM-2026-0001"); got["next_due"] != "2026-03-04" || got["next_step"] != "Termin mit dem Mieter abstimmen" {
		t.Fatalf("nächste Frist: %v %v", got["next_due"], got["next_step"])
	}
	for _, no := range []string{"30", "40", "50"} {
		e.must(stepObject, "done", map[string]any{"id": "1000|SM-2026-0001|" + no})
	}
	e.must(processObject, "acknowledge", map[string]any{"id": "1000|SM-2026-0001"})
	e.must(processObject, "note", map[string]any{"id": "1000|SM-2026-0001", "data": map[string]any{"entry_kind": "CALL", "entry_text": "Gretchen: trocken"}})
	if err := upd(map[string]any{"status": "DONE", "resolution": "SOLVED"}); err != nil {
		t.Fatal(err)
	}
	if got = e.get("SM-2026-0001"); got["closed_on"] != "2026-03-04" || got["next_due"] != nil || got["closed_by"] != "mephisto" {
		t.Fatalf("Abschluss: %v", got)
	}
	if n := e.count("SELECT COUNT(*) FROM process__entry WHERE process_id = 'SM-2026-0001' AND kind = 'CALL'"); n != 1 {
		t.Fatalf("Telefonat: %d", n)
	}
	if n := e.count("SELECT COUNT(*) FROM process__event WHERE process_id = 'SM-2026-0001' AND event IN ('CREATED', 'STEP_DONE', 'ACKNOWLEDGED', 'CLOSED')"); n != 8 {
		t.Fatalf("Journal: %d", n)
	}
	// Wieder öffnen: Wiedervorlage bleibt Pflicht, Abschlussdaten weg.
	if err := upd(map[string]any{"status": "IN_WORK", "follow_up": "2026-03-20"}); err != nil {
		t.Fatal(err)
	}
	// nächste Frist: der freiwillige Schritt „Versicherung melden“ (Eröffnung + 7) ist noch offen
	if got = e.get("SM-2026-0001"); got["closed_on"] != nil || got["next_due"] != "2026-03-09" {
		t.Fatalf("wieder offen: %v %v", got["closed_on"], got["next_due"])
	}
	if n := e.count("SELECT COUNT(*) FROM process__event WHERE event = 'REOPENED'"); n != 1 {
		t.Fatalf("REOPENED: %d", n)
	}
}

// TestTimerAndViews: Wiedervermietung mit Stichtag, Übergabe an Wagner (Hinweis),
// Zeitgeber (heute fällig, überschritten, eskaliert an den Vertreter, ohne
// Bewegung), Meldung an den Event-Dispatcher, Sichten und eigene Hinweise.
func TestTimerAndViews(t *testing.T) {
	e := setup(t)
	e.at("2026-03-02")
	if r := e.must(setupObject, "setupCompany", map[string]any{"company": "1000"}); toInt(r["texts"]) != 2*len(textDefaults) ||
		!e.h.refs["TextAssignment|Process|1000|FEEDBACK"] {
		t.Fatalf("Texte: %v", r)
	}
	if r := e.must(setupObject, "setupCompany", map[string]any{"company": "1000"}); toInt(r["texts"]) != 0 {
		t.Fatalf("Texte beim zweiten Lauf: %v", r)
	}
	p := e.create(processObject, map[string]any{"company_code": "1000", "process_type": "WIEDERVERM", "subject": "Kündigung Marthe Schwerdtlein",
		"event_date": "2026-05-31", "assignee": "wagner", "deputy": "faust"})
	if p["process_id"] != "WV-2026-0001" {
		t.Fatalf("Nummer: %v", p["process_id"])
	}
	st := e.steps("WV-2026-0001")
	if st[1]["due_date"] != "2026-05-01" || st[6]["due_date"] != "2026-05-31" || st[9]["due_date"] != "2026-11-27" {
		t.Fatalf("Stichtag: %v %v %v", st[1]["due_date"], st[6]["due_date"], st[9]["due_date"])
	}
	// Stichtag verschoben → Fristen der Schritte zum Stichtag mit.
	e.must(processObject, "update", map[string]any{"id": "1000|WV-2026-0001", "data": map[string]any{"event_date": "2026-06-30"}})
	if st = e.steps("WV-2026-0001"); st[6]["due_date"] != "2026-06-30" {
		t.Fatalf("verschoben: %v", st[6]["due_date"])
	}
	// Eigener Vorgang von Mephisto ohne Fristen der Art außer Wiedervorlage.
	e.create(processObject, map[string]any{"company_code": "1000", "process_type": "ANFRAGE", "subject": "Frage zum Hexenkessel", "follow_up": "2026-04-01"})

	list := func(view string) []map[string]any {
		return items(e.must(processObject, "list", map[string]any{"query": map[string]any{"company_code": "1000", "view": view}}))
	}
	// Wagner: Hinweis „zugewiesen“, nur seine Hinweise.
	restore := e.as("wagner")
	n := items(e.must(noticeObject, "list", map[string]any{"query": map[string]any{}}))
	if len(n) != 1 || n[0]["kind"] != noticeAssigned {
		t.Fatalf("Hinweise Wagner: %v", n)
	}
	if mine := list("MEINE"); len(mine) != 1 || mine[0]["process_id"] != "WV-2026-0001" {
		t.Fatalf("Meine (Wagner): %v", mine)
	}
	if today := list("MEINE_HEUTE"); len(today) != 0 {
		t.Fatalf("heute: %v", today)
	}
	e.must(noticeObject, "deactivate", map[string]any{"id": n[0]["notice_id"]})
	restore()
	if _, err := e.call(noticeObject, "deactivate", map[string]any{"id": n[0]["notice_id"]}); err == nil {
		t.Fatal("fremder Hinweis")
	}

	// 9. März: Schritt 10 (Eröffnung + 7) heute fällig.
	e.at("2026-03-09")
	r, err := e.m.checkDue(e.ctx)
	if err != nil || r.Due != 1 || r.Overdue != 0 {
		t.Fatalf("fällig: %+v %v", r, err)
	}
	if again, _ := e.m.checkDue(e.ctx); again.Due != 0 {
		t.Fatalf("zweimal gemeldet: %+v", again)
	}
	restore = e.as("wagner")
	if today := list("MEINE_HEUTE"); len(today) != 1 || today[0]["signal"] != "heute" {
		t.Fatalf("heute (Wagner): %v", today)
	}
	restore()
	// 11. März: überschritten; 17. März: eskaliert (7 Tage) an Faust und Wagner.
	e.at("2026-03-11")
	if r, _ = e.m.checkDue(e.ctx); r.Overdue != 1 {
		t.Fatalf("überschritten: %+v", r)
	}
	e.at("2026-03-17")
	if r, _ = e.m.checkDue(e.ctx); r.Escalated != 1 || r.Idle != 2 { // beide Vorgänge ohne Bewegung
		t.Fatalf("eskaliert: %+v", r)
	}
	// auch die Anfrage (Schritt 10 seit 4. März, Eskalation nach 3 Tagen)
	if esc := list("ESKALIERT"); len(esc) != 2 || esc[1]["signal"] != "eskaliert" {
		t.Fatalf("Sicht eskaliert: %v", esc)
	}
	if n := e.count("SELECT COUNT(*) FROM process__notice WHERE kind = ? AND user_id IN ('faust', 'wagner')", noticeEscalated); n != 2 {
		t.Fatalf("Eskalation: %d", n)
	}
	if over := list("UEBERFAELLIG"); len(over) != 2 {
		t.Fatalf("überfällig: %v", over)
	}
	// Bestätigt und Schritt 10 erledigt → nächste Frist ist das Inserat (Eröffnung + 14);
	// sie liegt weiter in der Vergangenheit, die Stufe bleibt.
	e.must(processObject, "acknowledge", map[string]any{"id": "1000|WV-2026-0001"})
	e.must(stepObject, "done", map[string]any{"id": "1000|WV-2026-0001|10"})
	e.must(processObject, "note", map[string]any{"id": "1000|WV-2026-0001", "data": map[string]any{"new_follow_up": "2026-03-25"}})
	if got := e.get("WV-2026-0001"); got["next_due"] != "2026-03-16" || got["next_step"] != "Inserat und Interessenten" || toInt(got["escalation"]) != levelEscalate {
		t.Fatalf("nach Erledigung: %v %v %v", got["next_due"], got["next_step"], got["escalation"])
	}
	// Alle Fristen in der Zukunft → Stufe 0.
	for _, no := range []string{"30"} {
		e.must(stepObject, "done", map[string]any{"id": "1000|WV-2026-0001|" + no})
	}
	if got := e.get("WV-2026-0001"); toInt(got["escalation"]) != 0 {
		t.Fatalf("Stufe nach Erledigung: %v (%v)", got["escalation"], got["next_due"])
	}
	// Journal an den Event-Dispatcher, genau einmal.
	pushed, err := e.m.pushEvents(context.Background())
	if err != nil || pushed == 0 {
		t.Fatalf("gemeldet: %d %v", pushed, err)
	}
	if again, _ := e.m.pushEvents(context.Background()); again != 0 {
		t.Fatalf("doppelt gemeldet: %d", again)
	}
	var escalated bool
	for _, ev := range e.h.events {
		if ev["object"] == eventObject && ev["action"] == "ESCALATED" && ev["entity_id"] == "1000|WV-2026-0001" {
			escalated = true
		}
	}
	if !escalated {
		t.Fatalf("Ereignisse: %v", e.h.events)
	}
	since := items(e.must(eventObject, "list", map[string]any{"query": map[string]any{"since": pushed - 2}}))
	if len(since) != 2 {
		t.Fatalf("ab Nummer: %d", len(since))
	}
}

// TestOwnerMeeting: Eigentümerversammlung aus Sicht des Eigentümers – Perspektive
// aus der Art, Schritte zum Versammlungstermin, Partner in Rollen, Sicht je Perspektive.
func TestOwnerMeeting(t *testing.T) {
	e := setup(t)
	e.at("2026-04-01")
	e.h.partners["BP-BLOCKSBERG"] = "WEG-Verwaltung Blocksberg"
	e.h.refs["BusinessEntity|1000|WE-HARZ"] = true
	e.create(processObject, map[string]any{"company_code": "1000", "process_type": "ETV_EIG", "subject": "22. Eigentümerversammlung Brockenweg",
		"event_date": "2026-04-28", "entity_id": "WE-HARZ"})
	got := e.get("EE-2026-0001")
	if got["perspective"] != "EIGENTUEMER" || got["next_due"] != "2026-04-04" {
		t.Fatalf("Versammlung: %v %v", got["perspective"], got["next_due"])
	}
	st := e.steps("EE-2026-0001")
	if st[2]["due_date"] != "2026-04-25" || st[5]["due_date"] != "2026-05-23" {
		t.Fatalf("Schritte: %v %v", st[2]["due_date"], st[5]["due_date"])
	}
	e.create(partnerObject, map[string]any{"company_code": "1000", "process_id": "EE-2026-0001", "role_code": "admin", "partner_id": "BP-BLOCKSBERG"})
	_, err := e.call(partnerObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "process_id": "EE-2026-0001", "role_code": "ADMIN", "partner_id": "BP-NIEMAND"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannter Partner")
	e.create(viewObject, map[string]any{"company_code": "1000", "code": "als_eigentuemer", "name": "Als Eigentümer", "perspective": "eigentuemer"})
	if own := items(e.must(processObject, "list", map[string]any{"query": map[string]any{"view": "ALS_EIGENTUEMER"}})); len(own) != 1 {
		t.Fatalf("Sicht Eigentümer: %v", own)
	}
	_, err = e.call(viewObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "X", "name": "x", "status_kinds": "FERTIG"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannte Status-Art")
	_, err = e.call(typeStepObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "process_type": "ETV_EIG", "step_no": 80, "name": "x", "due_basis": "OPEN", "due_days": -3}})
	expect(t, err, sdk.ErrInvalidArgument, "negative Tage ab Eröffnung")
}

// TestSubProcesses: Teilvorgang mit eigener Nummer seiner Art, Verweis auf den
// übergeordneten Vorgang und übernommenen Bezügen; Frist der Teilvorgänge am
// übergeordneten (alle Ebenen), Abschluss erst nach den Teilvorgängen, keine Kreise.
func TestSubProcesses(t *testing.T) {
	e := setup(t)
	e.at("2026-04-01")
	e.h.partners["BP-MARTHE"] = "Marthe Schwerdtlein"
	e.h.refs["RentObject|1000|AK-02"] = true
	e.create(processObject, map[string]any{"company_code": "1000", "process_type": "ANFRAGE", "subject": "Auszug Marthe",
		"partner_id": "BP-MARTHE", "object_id": "AK-02", "follow_up": "2026-04-30", "answer_due": "2026-04-30"})
	child := e.create(processObject, map[string]any{"company_code": "1000", "process_type": "SCHADEN", "subject": "Malerarbeiten",
		"parent_id": "AF-2026-0001"})
	if child["process_id"] != "SM-2026-0001" || child["parent_id"] != "AF-2026-0001" || child["partner_id"] != "BP-MARTHE" || child["object_id"] != "AK-02" {
		t.Fatalf("Teilvorgang: %v", child)
	}
	grand := e.create(processObject, map[string]any{"company_code": "1000", "process_type": "ANFRAGE", "subject": "Angebot Maler",
		"parent_id": "SM-2026-0001", "follow_up": "2026-04-03"})
	if grand["process_id"] != "AF-2026-0002" {
		t.Fatalf("Unter-Teilvorgang: %v", grand)
	}
	if p := e.get("AF-2026-0001"); p["sub_due"] != "2026-04-02" { // erster Schritt der Schadensmeldung
		t.Fatalf("Frist der Teilvorgänge: %v", p["sub_due"])
	}
	subs := items(e.must(processObject, "list", map[string]any{"query": map[string]any{"company_code": "1000", "parent_id": "AF-2026-0001"}}))
	if len(subs) != 1 || subs[0]["process_id"] != "SM-2026-0001" {
		t.Fatalf("Teilvorgänge: %v", subs)
	}
	upd := func(id string, data map[string]any) error {
		_, err := e.call(processObject, "update", map[string]any{"id": "1000|" + id, "data": data})
		return err
	}
	expect(t, upd("AF-2026-0001", map[string]any{"parent_id": "AF-2026-0002"}), sdk.ErrInvalidArgument, "Kreis")
	expect(t, upd("SM-2026-0001", map[string]any{"parent_id": "XX-2026-0001"}), sdk.ErrInvalidArgument, "unbekannter übergeordneter")

	closeIt := func(id string) error {
		for _, s := range e.steps(id) {
			if s["status"] == stepOpen {
				e.must(stepObject, "skip", map[string]any{"id": "1000|" + id + "|" + crud.Str(s["step_no"])})
			}
		}
		return upd(id, map[string]any{"status": "DONE", "resolution": "SOLVED"})
	}
	expect(t, closeIt("AF-2026-0001"), sdk.ErrInvalidArgument, "offener Teilvorgang")
	if err := closeIt("AF-2026-0002"); err != nil {
		t.Fatal(err)
	}
	if p := e.get("SM-2026-0001"); p["sub_due"] != nil {
		t.Fatalf("Frist nach Abschluss des Unter-Teilvorgangs: %v", p["sub_due"])
	}
	if err := closeIt("SM-2026-0001"); err != nil {
		t.Fatal(err)
	}
	if p := e.get("AF-2026-0001"); p["sub_due"] != nil {
		t.Fatalf("Frist nach Abschluss: %v", p["sub_due"])
	}
	_, err := e.call(processObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "process_type": "ANFRAGE",
		"subject": "zu spät", "parent_id": "SM-2026-0001"}})
	expect(t, err, sdk.ErrInvalidArgument, "abgeschlossener übergeordneter")
	if err := closeIt("AF-2026-0001"); err != nil {
		t.Fatal(err)
	}
}
