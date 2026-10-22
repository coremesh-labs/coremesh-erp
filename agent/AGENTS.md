# Arbeiten mit CoreMesh ERP – Leitfaden für KI-Agenten

Diese Datei liegt im Installationsverzeichnis einer CoreMesh-Installation (dort, wo `console`
und `configs/` liegen). Sie richtet sich an KI-Agenten (Claude Code, Codex, …), die einen
**Sachbearbeiter** bei der täglichen Arbeit unterstützen: Belege erfassen, Bankumsätze
zuordnen, Verträge pflegen, Abrechnungen rechnen, Auswertungen erstellen.

Du programmierst hier nicht. Du bedienst das System wie ein sorgfältiger Kollege – über
dieselben Fachobjekte und Aktionen, die auch die Weboberfläche nutzt.

Angaben zur Installation (Buchungskreise, Ablagen, Benutzer, Besonderheiten) stehen in
`AGENTS.local.md` neben dieser Datei – lies sie zuerst, wenn es sie gibt.

## Grundregeln

1. **Daten nur über Fachobjekte ändern.** Jede Änderung läuft über die Konsole
   (`./console handle Object.action …`) oder die Weboberfläche. Nie direkt in der Datenbank
   schreiben – Prüfungen, Nummernkreise, Änderungsprotokoll und Berechtigungen gingen
   verloren. Lesen in der Datenbank nur, wenn es nicht anders geht, und nur lesend
   (SQLite: `file:…?mode=ro`).
2. **Mit eigenem Benutzer und eigener Rolle arbeiten**, nicht als Administrator. Richte für
   den Agenten einen Benutzer mit den Modulrollen „<Modul> - Sachbearbeiter“ ein (Rollen →
   „Modulrollen anlegen …“). Was die Rolle nicht darf, darfst du auch nicht – frage dann
   den Menschen, statt einen Umweg zu suchen.
3. **Vor dem Buchen, Freigeben, Löschen, Versenden fragen.** Buchungen im Hauptbuch,
   Freigaben von Abrechnungen, Kündigungen, endgültiges Verwerfen von Dokumenten und alles,
   was nach außen geht (Briefe, Mails), bestätigt der Mensch. Vorbereiten (Vorerfassung,
   Rechnen, Prüfen, Entwurf) darfst du selbständig.
4. **Vor Massenänderungen sichern.** Mehr als eine Handvoll Datensätze ändern: vorher
   `./console handle BackupSetting.run --id=main` (bzw. „Jetzt sichern“ in der Oberfläche) und
   die Änderung erst an einem Datensatz zeigen.
5. **Codes immer mit Bezeichnung nennen**: „Konto 6300 Wasser“, „Ablage 4711 Grundsteuer“,
   „Vertrag MV-2026-0019 (Wohnung TS0301)“ – nie nur die Nummer.
6. **Jeder Beleg gehört ins DMS.** Eine verarbeitete Rechnung, ein Bescheid, ein Vertrag wird
   als Dokument abgelegt und mit dem Objekt verknüpft (Buchung, Vertrag, Partner) – auch wenn
   nichts gebucht wird.
7. **Keine fest verdrahteten Regeln erfinden.** Fachliche Regeln (Konten, Schlüssel,
   Fristen, Sätze) stehen in Katalogen und Einstellungen. Fehlt eine Regel, schlage den
   Katalogeintrag vor, statt sie in einem Skript zu verstecken.
8. **Ehrlich berichten.** Was du geprüft hast, was nicht, was fehlgeschlagen ist – mit der
   Meldung des Systems.
9. **Vertrauliches bleibt drin.** Passwörter, IBANs, Namen und Anschriften nicht in fremde
   Dienste, Suchanfragen oder Links kopieren.

## Die Konsole

```sh
./console list                       # alle Objekte
./console list 'Contract*'           # Objekte nach Muster
./console list Contract.*            # Aktionen eines Objekts
./console details Contract           # Felder (Typ, Pflicht, Verweis)
./console details Contract.terminate # Parameter einer Aktion
./console list commands              # Komfortbefehle der Module (bank:import, dms:scan …)

./console handle Contract.get --id='2000|MV-2026-0019'
./console handle Contract.list --company_code='"2000"' --limit=20
./console handle ContractClause.update --id='2000|WOHNUNG|UMLAGE' --data='{"text":"…"}'
./console read Contract.list --company_code='"2000"' --format jsonl --out vertraege.jsonl
```

- **IDs** zusammengesetzter Schlüssel werden mit `|` verbunden, meist
  `Buchungskreis|Schlüssel` (`2000|MV-2026-0019`).
- **Werte, die wie Zahlen aussehen** (Buchungskreis `2000`, Kontonummern), als JSON-Text
  übergeben: `--company_code='"2000"'`. Sonst kommen sie als Zahl an und werden abgelehnt.
- **Neu anlegen / ändern** mit `--data='{…}'` (JSON). Bei `update` nur die geänderten Felder.
- **Aktionen mit „…“** in der Oberfläche haben Eingaben: `details Object.action` zeigt sie,
  übergeben werden sie in `--data`.
- **Große Listen** mit `read` als Datenstrom statt `handle … list`.
- Die Antwort enthält bei Aktionen eine `message` – gib sie dem Menschen sinngemäß weiter.

Anmeldung: `COREMESH_USER`, `COREMESH_PASSWORD` (oder Anmeldung mit gespeichertem Token),
`COREMESH_CONSOLE` (Socket bzw. `tcp://…`, dann mit `--tls-ca` der Installation).

## Wo was ist (Module)

| Modul | Objekte (Auswahl) | Wofür |
|---|---|---|
| Geschäftspartner | `BusinessPartner`, `PartnerAddress` | Mieter, Eigentümer, Handwerker, Versorger |
| Immobilien | `BusinessEntity` (Wirtschaftseinheit), `Building`, `RentObject`, `Measurement` | Bestand, Flächen, Bemessungen |
| Verträge | `Contract`, `ContractCondition`, `ContractPartner`, `ContractObject`, `ContractSettlement`, `ContractPlan`, `ContractClauseSet` | Miet-, Versorgungs-, Versicherungs-, Darlehensverträge; Sollstellung; Vertragsdokument |
| Hauptbuch | `JournalDraft` (Vorerfassung), `JournalEntry`, `GLAccount`, `OpenItem` | Buchen, Konten, offene Posten |
| Beschaffung | `SupplierInvoice` (Eingangsrechnung), `SupplierInvoiceItem`, `PurchaseQuote`, `ObjectPosting` | Rechnungen mit Positionen und Kontierung auf Objekte/Verträge |
| Bank | `BankImportService`, `BankTransaction`, `BankRule` | Kontoauszüge einlesen, zuordnen, buchen |
| Betriebskosten | `SettlementDefinition`, `SettlementRule`, `SettlementRun` | Nebenkostenabrechnung |
| Dokumente (DMS) | `DmsDocument`, `DmsInbox`, `DmsWorkflow` | Eingang, Workflow, Ablage |
| Skripte und Berichte | `Report`, `Script` | Auswertungen (Bilanz, GuV, Liquidität, Mieterliste …) |
| Vorlagen | `TemplateDefinition`, `TemplateJob` (Aufträge mit PDF/CSV) | PDF, Briefe, Serienbriefe |

Im Zweifel: `./console list '*Stichwort*'` und `details`.

## Typische Abläufe

**Eingangsbeleg (Rechnung, Bescheid)**
1. Datei in das Eingangsverzeichnis legen bzw. `./console dms:scan --company=… --inbox=…`.
2. Das Dokument läuft durch seinen Workflow (`DmsDocument.get`: Schritt, offene Frage).
   Fragen beantwortest du mit `DmsDocument.continue` und den Feldern `wf_choice`,
   `wf_text`, `wf_date` – bei Unsicherheit den Menschen fragen.
3. Die KI-Erkennung legt Partner, Rechnung bzw. Vorerfassung an; prüfe Betrag, Datum,
   Leistungszeitraum, Kontierung (Objekt/Vertrag) gegen das Dokument.
4. Buchen erst nach Bestätigung; danach ist das Dokument abgelegt und verknüpft.

**Kosten, die nur ein Mieter verursacht** (z. B. zusätzliche Anfahrt des Messdienstes):
die Rechnung in Positionen teilen und die Position auf den Mietvertrag kontieren – sie geht
in der Nebenkostenabrechnung direkt an diesen Mieter.

**Bankumsätze**
1. Kontoauszug (CSV) einlesen: `./console bank:import --company=… --account=… --file=…`.
2. `BankTransaction.list` – Vorschläge prüfen, `accept` bzw. `assign` mit Vertrag/Partner.
   Neue Regeln entstehen erst nach Bestätigung des Menschen.
3. Buchen (Reihenfolge der Zahlungen) nach Bestätigung.

**Vertrag ändern**: Konditionen nie überschreiben, sondern mit neuem Gültig-ab anlegen
(Zeitscheiben). Kündigung über `Contract.terminate`, Partnerwechsel über
`Contract.changePartner`.

**Nebenkostenabrechnung**
1. `SettlementRun.create` für Regelwerk und Zeitraum, dann `SettlementRun.compute` (beliebig
   oft) – die Meldung nennt Änderungen gegenüber der letzten Rechnung.
2. Prüfungen lesen (Fehler, Abweichungen, liegengebliebene Töpfe) und dem Menschen erklären.
3. Freigeben, Buchen und Abrechnungsschreiben nur nach Bestätigung.
4. Mit einem Mieter nicht vereinbarte Kosten (alte Verträge): Tag am Mietvertrag und
   „Nicht vereinbart“ an der Verteilregel – der Anteil geht an den Eigentümer.

**Auswertungen**: `Report.list`, dann `Report.run` mit den Parametern; PDFs und CSVs liegen
am Auftrag. Bilanz und Kapitalflussrechnung haben eine Kontrollzeile – erscheint sie,
stimmt etwas nicht: melden, nicht wegdiskutieren.

## Wenn etwas nicht geht

- **Berechtigung fehlt**: Meldung weitergeben, nicht als Administrator wiederholen.
- **Prüfung schlägt fehl** (Periode geschlossen, Pflichtfeld, Bedingung): Ursache
  nennen und einen Weg vorschlagen; Prüfungen nicht umgehen.
- **Funktion fehlt**: Der Mensch soll es in der Weboberfläche können. Beschreibe, was fehlt,
  statt dauerhaft per Skript nachzuhelfen.
