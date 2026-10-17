package dms

import (
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Workflows (Form 2, seit 0.12.0): acht Schrittarten – drei warten auf den
// Benutzer, fünf sind Programme. Alle lesen und schreiben Felder des Dokuments,
// Bezüge und Variablen; Programmschritte machen Vorschläge (Herkunft, Punkte),
// entschieden wird im Benutzerschritt. Was ein Schritt tut, steht in seinen
// Einstellungen (Spalten des Schritts), Bedingungen (DmsStepCondition) und
// Zuordnungen (DmsStepMapping); die Optionen (DmsStepOption) bestimmen den
// nächsten Schritt und die Knöpfe.

const (
	kInput     = "INPUT"     // Benutzer: Eingabe (Text, Datum, Zahl, Ja/Nein)
	kSelect    = "SELECT"    // Benutzer: Auswahl aus einer Liste
	kForm      = "FORM"      // Benutzer: Maske eines Objects, mit KI vorbelegt, anlegen
	kCommand   = "COMMAND"   // Programm: Befehl aus dem Katalog DmsCommand
	kAI        = "AI"        // Programm: KI-Anfrage (Modul ai)
	kAction    = "ACTION"    // Programm: Aktion eines Objects aufrufen
	kRecognize = "RECOGNIZE" // Programm: Erkennungsregeln (DmsHint) → Vorschläge
	kCheck     = "CHECK"     // Programm: Bedingungen prüfen → Ja/Nein
)

// Ergebnisse eines Schritts.
const (
	oSelected = "SELECTED"
	oEntered  = "ENTERED"
	oYes      = "YES"
	oNo       = "NO"
	oCreated  = "CREATED"
	oCreate   = "CREATE"
	oSuccess  = "SUCCESS"
	oFailed   = "FAILED"
	oSkipped  = "SKIPPED"
	oEmpty    = "EMPTY"
	oValue    = "VALUE"
)

// Eingabearten (INPUT).
const (
	inText   = "TEXT"
	inDate   = "DATE"
	inNumber = "NUMBER"
	inYesNo  = "YESNO"
)

// Quellen der Auswahl (SELECT, auch KI-Auswahl).
const (
	srcObject   = "OBJECT"   // Liste eines Objects (Object.Action) mit Filtern
	srcList     = "LIST"     // feste Werte (Zuordnungen „Wert“)
	srcTree     = "TREE"     // Ablage Ebene für Ebene
	srcCategory = "CATEGORY" // kaufmännische Objekte der Dokumentkategorie
)

// Feld schon belegt (Benutzerschritte).
const (
	ifSetAsk  = "ASK"  // fragen, vorbelegt mit dem Wert
	ifSetSkip = "SKIP" // überspringen
)

// Übernahme der Ergebnisse von Programmschritten.
const (
	applyPropose = "PROPOSE" // Vorschlag für den Benutzerschritt des Felds
	applySet     = "SET"     // direkt setzen (nur, wenn das Feld leer ist)
)

// Rollen der Bedingungen.
const (
	condApplies = "APPLIES" // Schritt gilt nur, wenn alle erfüllt sind (sonst übersprungen)
	condCheck   = "CHECK"   // Prüfen: alle erfüllt = Ja
	condPrefill = "PREFILL" // Maske: Positionen aus der Vorbelegung (z. B. Vertrag), sonst nur der Kopf
)

// Rollen der Zuordnungen.
const (
	mapFilter         = "FILTER"          // Auswahl: Filter Feld = Wert (Platzhalter), optional je Kategorie
	mapValue          = "VALUE"           // Auswahl aus festen Werten: Wert = Bezeichnung
	mapLink           = "LINK"            // Auswahl: Link „anlegen“ im neuen Fenster (Object = Text)
	mapParam          = "PARAM"           // Aktion: Parameter = Wert
	mapResult         = "RESULT"          // Aktion: Feld der Antwort → Ziel
	mapRequest        = "REQUEST"         // Maske: Object = KI-Anfrage
	mapRequestPrefill = "REQUEST_PREFILL" // Maske: Object = KI-Anfrage, wenn die Positionen aus der Vorbelegung kommen
	mapPrefill        = "PREFILL"         // Maske: Object = Object.Action der Vorbelegung (z. B. SupplierInvoice.contractDraft)
	mapDefault        = "DEFAULT"         // Maske: Feld (gruppe.feld) = Vorgabe
	mapHint           = "HINT"            // Maske: Feld = Wert aus dem Dokument (nur leere Felder)
)

// Operatoren der Bedingungen.
const (
	opEq       = "EQ"
	opNe       = "NE"
	opEmpty    = "EMPTY"
	opNotEmpty = "NOT_EMPTY"
	opIn       = "IN"
	opNotIn    = "NOT_IN"
	opHas      = "HAS"
	opNotHas   = "NOT_HAS"
	opContains = "CONTAINS"
	opGe       = "GE"
	opLe       = "LE"
)

var (
	kindOptions = []metamodel.Option{
		{Value: kInput, Label: "Benutzer: Eingabe (Text, Datum, Zahl, Ja/Nein)", Short: "Eingabe"},
		{Value: kSelect, Label: "Benutzer: Auswahl aus Liste", Short: "Auswahl"},
		{Value: kForm, Label: "Benutzer: Maske – Objekt anlegen (mit KI vorbelegt)", Short: "Maske"},
		{Value: kCommand, Label: "Programm: Befehl (Katalog Befehle)", Short: "Befehl"},
		{Value: kAI, Label: "Programm: KI-Anfrage", Short: "KI"},
		{Value: kAction, Label: "Programm: Aktion eines Objects aufrufen", Short: "Aktion"},
		{Value: kRecognize, Label: "Programm: Erkennen (Erkennungsregeln → Vorschläge)", Short: "Erkennen"},
		{Value: kCheck, Label: "Programm: Prüfen (Bedingungen → Ja/Nein)", Short: "Prüfen"},
	}
	outcomeOptions = []metamodel.Option{
		{Value: oSelected, Label: "Ausgewählt", Short: "ausgewählt"},
		{Value: oEntered, Label: "Eingegeben", Short: "eingegeben"},
		{Value: oYes, Label: "Ja"},
		{Value: oNo, Label: "Nein"},
		{Value: oCreated, Label: "Angelegt (Maske)", Short: "angelegt"},
		{Value: oCreate, Label: "Anlegen gewählt (Knopf der Auswahl)", Short: "anlegen"},
		{Value: oSuccess, Label: "Programmschritt erfolgreich", Short: "erfolgreich"},
		{Value: oFailed, Label: "Programmschritt nicht erfolgreich", Short: "fehlgeschl."},
		{Value: oSkipped, Label: "Übersprungen (Bedingung, Feld schon belegt oder Knopf)", Short: "übersprungen"},
		{Value: oEmpty, Label: "Auswahl leer (keine Werte)", Short: "leer"},
		{Value: oValue, Label: "Wert gleich (Spalte Wert)", Short: "Wert"},
	}
	inputTypeOptions = []metamodel.Option{
		{Value: inText, Label: "Text"}, {Value: inDate, Label: "Datum"}, {Value: inNumber, Label: "Zahl"},
		{Value: inYesNo, Label: "Ja/Nein (nur Knöpfe)"},
	}
	sourceKindOptions = []metamodel.Option{
		{Value: srcObject, Label: "Liste eines Objects (mit Filtern)"},
		{Value: srcList, Label: "Feste Werte (Zuordnungen „Wert“)"},
		{Value: srcTree, Label: "Ablage Ebene für Ebene"},
		{Value: srcCategory, Label: "Kaufmännische Objekte der Dokumentkategorie"},
	}
	ifSetOptions = []metamodel.Option{
		{Value: ifSetAsk, Label: "Fragen, vorbelegt mit dem Wert"},
		{Value: ifSetSkip, Label: "Überspringen"},
	}
	applyOptions = []metamodel.Option{
		{Value: applyPropose, Label: "Als Vorschlag für den Benutzerschritt"},
		{Value: applySet, Label: "Direkt setzen (nur leere Felder)"},
	}
	condRoleOptions = []metamodel.Option{
		{Value: condApplies, Label: "Schritt gilt nur, wenn"},
		{Value: condCheck, Label: "Prüfen (Ja, wenn alle erfüllt)"},
		{Value: condPrefill, Label: "Maske: Positionen aus der Vorbelegung, wenn"},
	}
	mapRoleOptions = []metamodel.Option{
		{Value: mapFilter, Label: "Auswahl: Filter"},
		{Value: mapValue, Label: "Auswahl: fester Wert"},
		{Value: mapLink, Label: "Auswahl: Link „anlegen“ (neues Fenster)"},
		{Value: mapParam, Label: "Aktion: Parameter"},
		{Value: mapResult, Label: "Aktion: Ergebnis → Ziel"},
		{Value: mapRequest, Label: "Maske: KI-Anfrage je Object"},
		{Value: mapRequestPrefill, Label: "Maske: KI-Anfrage bei Positionen aus der Vorbelegung"},
		{Value: mapPrefill, Label: "Maske: Vorbelegung (Object.Action)"},
		{Value: mapDefault, Label: "Maske: Vorgabe"},
		{Value: mapHint, Label: "Maske: Wert aus dem Dokument (leere Felder)"},
	}
	opOptions = []metamodel.Option{
		{Value: opEq, Label: "gleich"}, {Value: opNe, Label: "ungleich"},
		{Value: opEmpty, Label: "ist leer"}, {Value: opNotEmpty, Label: "ist nicht leer"},
		{Value: opIn, Label: "in Liste (Komma, * am Ende = beginnt mit)"}, {Value: opNotIn, Label: "nicht in Liste"},
		{Value: opHas, Label: "Liste enthält"}, {Value: opNotHas, Label: "Liste enthält nicht"},
		{Value: opContains, Label: "Text enthält"},
		{Value: opGe, Label: "Zahl ≥"}, {Value: opLe, Label: "Zahl ≤"},
	}
	// userKinds warten auf eine Eingabe.
	userKinds = map[string]bool{kInput: true, kSelect: true, kForm: true}
)

// positive: Ergebnis eines erfolgreichen Schritts der Art.
func positive(s *stepDef) string {
	switch s.Kind {
	case kSelect:
		return oSelected
	case kInput:
		if s.InputType == inYesNo {
			return oYes
		}
		return oEntered
	case kForm:
		return oCreated
	case kCheck:
		return oYes
	}
	return oSuccess
}

// buttonOutcomes: Ergebnisse, die ein Benutzerschritt als Knopf anbietet (der erste
// ist der Hauptknopf und erscheint immer, die anderen nur mit einer Option).
func buttonOutcomes(s *stepDef) []string {
	switch s.Kind {
	case kSelect:
		return []string{oSelected, oCreate, oSkipped}
	case kInput:
		if s.InputType == inYesNo {
			return []string{oYes, oNo}
		}
		return []string{oEntered, oSkipped}
	case kForm:
		return []string{oCreated, oSkipped}
	}
	return nil
}

// noValidate: Knopf ohne Eingaben (keine Pflichtprüfung).
func noValidate(outcome string) bool {
	return outcome == oSkipped || outcome == oCreate || outcome == oNo
}

// defaultButtonLabel: Text eines Knopfs ohne eigenen Text an der Option.
func defaultButtonLabel(s *stepDef, outcome string) string {
	switch outcome {
	case oSelected, oEntered:
		return "Weiter"
	case oYes:
		return "Ja"
	case oNo:
		return "Nein"
	case oCreated:
		if s.Confirm {
			return "Vorschau"
		}
		return "Anlegen"
	case oCreate:
		return "Anlegen"
	case oSkipped:
		return "Überspringen"
	}
	return outcome
}

// targetKey: Schlüssel eines Ziels für Vorschläge und Verlauf – Feld des Dokuments,
// „ref:<Object>“ (leer: ref), „var:<Name>“.
func targetKey(target, object, varName string) string {
	switch target {
	case "ref":
		if object != "" {
			return "ref:" + object
		}
		return "ref"
	case "var":
		return "var:" + varName
	}
	return target
}

// splitTarget: „ref:Contract“ → ref, Contract; „var:x“ → var, x; Feld → Feld, "".
func splitTarget(key string) (target, arg string) {
	if t, a, ok := strings.Cut(key, ":"); ok {
		return t, a
	}
	return key, ""
}
