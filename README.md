# CoreMesh ERP

Fachplugins auf Basis von [CoreMesh](../coremesh) für

- **Mietverwaltung** (Liegenschaften, Mietobjekte, Mietverträge, Nebenkosten),
- **private Vertragsverwaltung** (Versicherungen, Darlehen, Abos …),
- **Buchhaltung** (Hauptbuch, später Debitoren/Kreditoren, Anlagen).

Go-Modul: `github.com/camel/coremesh_erp`. Die Plugins nutzen ausschließlich die öffentliche
API des Kerns (`github.com/camel/coremesh/pkg/sdk/...`). `go.mod` verweist per `replace` auf das
Nachbarverzeichnis `../coremesh`.

```
C:\ext-git\
├── coremesh\        Kern: Host, Core-Plugins (iam, catalog, dbschema), WebServer, Console, Partner, Tags
└── coremesh-erp\    dieses Repository
    ├── cmd/plugins/<plugin>/main.go              ein Plugin = ein Prozess
    ├── cmd/plugins/<plugin>/internal/<modul>/    Fachcode (von außen nicht importierbar)
    ├── pkg/<plugin>api/                          öffentliche Schnittstelle für andere Plugins
    ├── configs/                                  Host-Konfiguration der ERP-Plugins
    ├── docs/                                     DDL und Entwürfe
    └── bin/plugins/<xx>/<name>-<version>-<os>-<arch>[.exe]
```

## Plugins

| Plugin | Modul (URL, Konsole) | Inhalt | Version |
|---|---|---|---|
| `ledger` | `ledger` (`/m/ledger`, `console ledger:…`) | Hauptbuch nach S/4HANA-Vorbild: Kontenpläne (SKA1/SKB1), Universal Journal (BKPF/ACDOCA), Vorerfassung, Periodensperre, Währungen und Tageskurse | 0.3.0 |

## Bauen, testen, starten

Unter Windows ohne `make`:

```powershell
.\build.ps1          # baut alle Plugins nach bin/plugins
.\build.ps1 -Test    # go vet + go test
.\build.ps1 -Run     # bauen und den Host des Kerns mit beiden Konfigurationen starten
```

Mit `make`: `make build`, `make test`, `make run`.

Der Host liest die Konfiguration des Kerns und dieses Repositories nacheinander:

```bash
cd ../coremesh
bin/host -config configs,../coremesh-erp/configs
```

`configs/10-ledger.yaml` trägt die Plugins ein und ergänzt `host.extra_plugin_dirs` um
`../coremesh-erp/bin/plugins`.

---

## Hauptbuch (`ledger`)

Zentrales General Ledger. Fachmodule buchen **synchron und mehrdimensional** in ein
Universal Journal. Gebuchte Belege sind unveränderlich; Korrekturen sind Stornobelege.

### Datenmodell

Vollständiges DDL: [docs/ledger_schema_postgres.sql](docs/ledger_schema_postgres.sql).
Maßgeblich ist das Atlas-Schema in `internal/ledger/schema.go`; ein Test hält beide synchron.
Tabellen tragen das Pflicht-Präfix `ledger__` des Plugins.

| Tabelle | Object | Analog SAP | Inhalt |
|---|---|---|---|
| `ledger__chart_of_accounts` | `ChartOfAccounts` | T004 | Kontenplan (SKR04, SKR25 …) |
| `ledger__account_master` | `GLAccount` | SKA1 | Sachkonto je Kontenplan: Nummer, Bezeichnung, Kontoart (`BALANCE_SHEET`, `PRIMARY_COST`, `SECONDARY_COST`, `REVENUE`, `NON_OPERATING`), aktiv |
| `ledger__account_company` | `GLAccountCompany` | SKB1 | Sachkonto im Buchungskreis: Kontowährung, Abstimmkonto (`NONE`, `CUSTOMER`, `SUPPLIER`, `ASSET`), alternative Kontonummer, Steuerkategorie, Buchungssperre |
| `ledger__company_config` | `LedgerCompanyConfig` | T001/FINSC | je Buchungskreis: führendes Ledger, Kontenplan, Hauswährung, Geschäftsjahresvariante, Kurstyp, **Modul-Mapping** (JSON) |
| `ledger__ledger` | `Ledger` | FINSC_LEDGER | `0L` führend (HGB), `2L` parallel (IFRS) |
| `ledger__fiscal_period_status` | `FiscalPeriod` | OB52 | Periode offen/gesperrt je Buchungskreis, Ledger, Jahr, Periode 1–16; **ohne Eintrag gesperrt** |
| `ledger__number_range` | – | NRIV | Belegnummernkreis je Buchungskreis und Jahr (ab `1000000001`) |
| `ledger__journal_entry_header` | `JournalEntry` | BKPF | Belegkopf: Nummer, Jahr, Periode, Daten, Belegart, Währungen, Kurs, Herkunft (Modul, Referenz), Storno |
| `ledger__journal_entry_item` | `JournalEntryItem` | ACDOCA | Einzelposten: Ledger, Konto, Soll/Haben, Betrag Beleg-/Hauswährung, Kostenstelle, Profit-Center, Segment, SD-, RENT-, Einkaufs- und freie Dimensionen |
| `ledger__draft_header`, `ledger__draft_item` | `JournalDraft`, `JournalDraftItem` | VBKPF/VBSEG | Vorerfassung manueller Buchungen |
| `ledger__currency` | `Currency` | TCURC/TCURX | Währung mit Nachkommastellen |
| `ledger__exchange_rate` | `ExchangeRate` | TCURR | Tageskurs je Kurstyp (`M`, `B`, `G`) und Währungspaar ab Gültigkeitsdatum, mit Umrechnungsfaktoren |

**Beträge** stehen als ganze Zahl in der kleinsten Einheit der Währung (EUR: Cent, JPY: Yen),
vorzeichenbehaftet wie in ACDOCA: Soll positiv, Haben negativ. Summe eines Belegs je Ledger = 0.

**Fremdschlüssel, auch rekursiv:**
- Positionen → Kopf, Konto (Kontenplan + Nummer), Ledger.
- **Storno ↔ Original** als Selbstbezug des Belegkopfs: `reversed_document_id` und
  `reversal_document_id` → `journal_entry_header.id`.
- **Vorerfassung ↔ Beleg:** `journal_entry_header.draft_id` → Vorerfassung und
  `draft_header.posted_document_id` → Beleg.
- Buchungskreise gehören dem Core-Plugin `iam` und werden über dessen Actions geprüft, nicht
  per Fremdschlüssel.

### Konsole: Kontenrahmen und Stammdaten laden

Über das Console-Plugin des Kerns (`console <modul>:<befehl>`):

```bash
console ledger:help
console ledger:load-coa --chart=SKR04                          # mitgelieferter Kontenrahmen
console ledger:load-coa --chart=SKR25 --file=./skr25.csv       # eigene Datei (JSON oder CSV), z. B. vollständiger Rahmen
console ledger:setup-company --company=1000 --chart=SKR25 --currency=EUR --year=2026
console ledger:load-rates --file=./kurse.csv                   # rate_type;from_currency;to_currency;valid_from;rate[;from_factor;to_factor]
console ledger:periods --company=1000 --year=2026 --from=13 --to=16 --status=CLOSED
```

- **Idempotent (Upsert):** Ein zweiter Lauf meldet „0 neu, 0 geändert, 29 unverändert“;
  geänderte Bezeichnungen werden aktualisiert.
- **Mitgeliefert** (`internal/ledger/coa/*.json`):
  - **SKR04** mit 29 Grundkonten der Klassen 0–7.
  - **SKR25** (Wohnungswirtschaft) mit 24 Konten: Sollmieten kalt, Erlösschmälerungen,
    Betriebskosten-Vorauszahlungen als erhaltene Anzahlungen, abgerechnete Betriebskosten,
    Instandhaltung, Mietkautionen (Treuhandkonto und Verbindlichkeit), Objektfinanzierung.
  - „SKR25“ ist eine interne Kennung, kein offizieller DATEV-Kontenrahmen. Der Inhalt ist
    ein repräsentativer Auszug in Anlehnung an den GdW-„Kontenrahmen der Wohnungswirtschaft“.
    DATEV bietet für Wohnungsunternehmen eigene Kontenrahmen auf Basis von SKR 03/04
    (angepasst an die JAbschlWUV). Den offiziellen Rahmen per `--file` laden.
- **`setup-company`:**
  - legt die Steuerung des Buchungskreises an,
  - ordnet alle aktiven Konten zu, mit den Vorschlägen für Abstimmkonto und Steuerkategorie
    aus der Kontenrahmen-Datei,
  - öffnet die Perioden 1–12 des Jahres.
- Format der JSON-Dateien:
  `{"chart_of_accounts_id": "SKR25", "name": "…", "accounts": [{"account_number": "6000", "name": "…", "account_type": "REVENUE", "account_group": "…", "reconciliation_type": "CUSTOMER", "tax_category": "OUTPUT_ONLY"}]}`.
  CSV mit denselben Spaltennamen in der Kopfzeile.

### Buchen aus Fachmodulen: `LedgerPosting`

Fachmodule nutzen den Client [`pkg/ledgerapi`](pkg/ledgerapi/ledgerapi.go) (Service
`LedgerPosting`, Actions `post`, `simulate`, `reverse`):

```go
gl := ledgerapi.New(env.Services)
res, err := gl.Post(ctx, ledgerapi.PostRequest{
    SourceModule: "RENT", SourceReference: "SOLL-2026-10-MV-0007",
    CompanyCode: "1000", DocumentType: "DR", PostingDate: "2026-10-01", Currency: "EUR",
    HeaderText: "Sollstellung Miete Oktober",
    Items: []ledgerapi.Item{
        {Account: "1200", Side: ledgerapi.Debit, Amount: "1250.00", Assignments: map[string]string{"contract": "MV-0007", "object": "WE-0001-0003"}},
        {Account: "6000", Side: ledgerapi.Credit, Amount: "1000.00", Assignments: map[string]string{"contract": "MV-0007", "object": "WE-0001-0003"}},
        {Account: "2800", Side: ledgerapi.Credit, Amount: "250.00", Assignments: map[string]string{"contract": "MV-0007", "object": "WE-0001-0003"}, Text: "BK-Vorauszahlung"},
    },
})
// res.DocumentNumber == "1000000001"; derselbe Aufruf noch einmal → res.Duplicate == true
```

`PostingService` (`internal/ledger/posting.go`) verarbeitet den Auftrag in einer Transaktion:

1. **Kopf und Recht:**
   - Modul, Buchungskreis, Belegwährung und mindestens zwei Positionen sind Pflicht.
   - Nötig ist `JournalEntry.post` im Buchungskreis.
   - Dazu `FiscalPeriod.post` und `DocumentType.post` mit passenden Feldwerten (siehe unten).
2. **Steuerung lesen** (`ledger__company_config`): Ledger, Kontenplan, Hauswährung, Kurstyp,
   Mapping.
3. **Periode prüfen:**
   - Geschäftsjahr und Periode ergeben sich aus dem Buchungsdatum (Variante K4).
   - Sonderperioden 13–16 nur mit Buchungsdatum im Dezember.
     In der Vorerfassung über das Feld „Sonderperiode“.
   - Die Periode muss in `ledger__fiscal_period_status` offen sein.
4. **Modul-Mapping:** Kontierungen des Moduls (`assignments`) werden in ACDOCA-Spalten
   übersetzt. Unbekannte Kontierungen werden abgelehnt.
5. **Positionen prüfen:**
   - Das Konto ist im Kontenplan aktiv, im Buchungskreis zugeordnet und nicht gesperrt.
   - Kontowährung: Ein Konto in Fremdwährung nimmt nur Belege in dieser Währung an.
   - **Abstimmkonten** brauchen einen Partner: Debitoren `sd_customer_id` oder
     `rent_contract_id`, Kreditoren `supplier_id`. Anlagen sind nur über die
     Anlagenbuchhaltung bebuchbar.
6. **Soll = Haben** in Belegwährung (Summe 0). Umrechnung in die Hauswährung mit dem
   Tageskurs am Buchungsdatum; die Rundungsdifferenz geht auf die betragsgrößte Position.
7. **Schreiben:** Belegnummer vergeben, Kopf (BKPF) und Einzelposten (ACDOCA) schreiben.
   **Idempotenz:** Gleiche `source_reference` je Buchungskreis und Modul liefert den
   vorhandenen Beleg.

**Modul-Mapping** (`module_field_mapping`, leer = Standard):

```json
{
  "SD":          {"sales_order": "sd_sales_order_id", "sales_org": "sd_sales_org", "customer": "sd_customer_id", "cost_center": "cost_center"},
  "RENT":        {"object": "rent_object_id", "contract": "rent_contract_id", "building": "dimension_custom_1", "tenant": "sd_customer_id"},
  "PROCUREMENT": {"purchase_order": "purchase_order_id", "supplier": "supplier_id", "cost_center": "cost_center"}
}
```

- Erlaubte Zielspalten sind nur die Kontierungsspalten von ACDOCA.
- Eine Spalte darf je Modul nur einmal belegt sein.
- Weitere Module lassen sich ohne Code ergänzen, indem man einen neuen Schlüssel einträgt.
- `MANUAL` (Vorerfassung) schreibt die Spalten direkt.

`simulate` prüft denselben Auftrag vollständig, ohne zu buchen, und zeigt die gemappten
Spalten und Hauswährungsbeträge. `reverse` storniert:
- vertauschte Seiten, negierte Beträge, gleiche Hauswährung,
- Storno-Datum nicht vor dem Original,
- höchstens einmal,
- kein Storno vom Storno.

### Manuelle Buchung: Vorerfassung (speichern, dann buchen)

In der Oberfläche unter **Hauptbuch → Belege → Vorerfassung**:

1. **Speichern:** Buchungskopf anlegen (Buchungskreis, Datum, Währung, Text), Positionen
   hinzufügen (Konto, Soll/Haben, Betrag, Kontierungen). Alles bleibt änderbar.
   Positionen lassen sich entfernen. Der Kopf zeigt laufend den Saldo Soll − Haben.
2. **Prüfen:** Vollständige Simulation über den `PostingService`.
3. **Buchen:** Der Beleg entsteht in derselben Transaktion:
   - Herkunft `MANUAL`, Referenz `DRAFT-<id>`,
   - die Vorerfassung bekommt Status `POSTED` und `posted_document_id`.

   Danach sind Vorerfassung und Beleg **nicht mehr änderbar**. Die Oberfläche blendet die
   Knöpfe aus, der Server lehnt Änderungen ab.
4. **Verwerfen:** Eine offene Vorerfassung bleibt als `DISCARDED` erhalten.

### Belegarten, Positionsarten und Feldstatus

Welche Kontierungsfelder eine Position braucht, bestimmen drei Dinge, wie in SAP:

| Steuerung | Tabelle | Wirkung |
|---|---|---|
| **Belegart** | `ledger__document_type` (`DocumentType`) | erlaubte Positionsarten, Referenz Pflicht |
| **Positionsart** (`item_type`, analog KOART) | Spalte in Vorerfassung und Universal Journal | `GL`, `CUSTOMER`, `SUPPLIER`, `TAX`, `ASSET` – aus dem Konto abgeleitet (Abstimmkonto, Steuerkonto) |
| **Feldstatusgruppe** des Kontos | `ledger__field_status_group` + `ledger__field_status` (`FieldStatusGroup`), Zuordnung am Sachkonto im Buchungskreis | je Feld `SUPPRESS` (ausblenden), `OPTIONAL`, `REQUIRED` |

Mitgelieferte Belegarten:

| Belegart | Positionsarten | Referenz Pflicht |
|---|---|---|
| SA Sachkontenbeleg | Sachkonto, Steuer | nein |
| DR Debitorenrechnung | Debitor, Sachkonto, Steuer | ja |
| DZ Debitorenzahlung | Debitor, Sachkonto | nein |
| KR Kreditorenrechnung | Kreditor, Sachkonto, Steuer | ja |
| KZ Kreditorenzahlung | Kreditor, Sachkonto | nein |
| AB Verrechnung/Storno | alle außer Anlage | nein |

Mitgelieferte Feldstatusgruppen:

| Gruppe | Inhalt |
|---|---|
| `STD` | alles optional |
| `BANK`, `TAX` | ohne Kontierung |
| `BALANCE` | Objekt und Vertrag optional |
| `CUSTOMER`, `SUPPLIER` | Partner; Lieferant Pflicht |
| `REVENUE` | Vertrieb und Profit-Center |
| `RENT_REVENUE` | Mietobjekt und Mietvertrag Pflicht |
| `COST` | Kostenstelle Pflicht |
| `OBJECT_COST` | Mietobjekt Pflicht |

- Die Kontenrahmen-Dateien bringen Vorschläge je Konto mit.
- Ohne Vorschlag leitet `setup-company` die Gruppe aus Kontoart, Abstimmkonto und
  Steuerkategorie ab.
- Ein erneuter Lauf von `setup-company` ergänzt fehlende Gruppen.

**Eine Stelle für alle Regeln** (`rules.go`): die Maske der Vorerfassung (`formState`), das
Speichern einer Position und das Buchen (auch aus Fachmodulen) prüfen dasselbe.

- Ausgeblendete Felder müssen leer sein, Muss-Felder gefüllt.
- Debitoren brauchen Kunde oder Mietvertrag, Kreditoren einen Lieferanten.

**Erfassung in der Oberfläche:**
- Ohne Konto zeigt die Position nur die Positionsfelder.
- Der Auswahldialog „Konto“ zeigt nur die Konten des Buchungskreises aus dem Buchungskopf,
  nicht gesperrt, mit Abstimmkonto und Feldstatusgruppe.
- Nach der Wahl erscheint unter „Kontierung“ genau das, was die Feldstatusgruppe verlangt,
  mit Hinweis, z. B. „Sachkonto · Feldstatusgruppe RENT_REVENUE – Mieterlöse (Objekt und
  Vertrag Pflicht)“.

### Konten sperren und Kontensperren je Periode

- **Sperren/Entsperren** am Sachkonto im Kontenplan (gilt in allen Buchungskreisen) und im
  Buchungskreis. Die Oberfläche bietet je nach Zustand nur die passende Aktion an.
- **Kontensperren je Periode** (`PeriodAccountLock`, analog Kontointervalle in OB52):
  Ausnahmen zum Periodenstatus für Kontenbereiche.
  - `CLOSED`: Konten sind gesperrt, obwohl die Periode offen ist.
  - `OPEN`: Konten bleiben buchbar, obwohl die Periode gesperrt ist.
  - Bei Widerspruch gilt `CLOSED`.
  - „Inaktivieren“ hebt eine Sperre auf.

```bash
console ledger:periods --company=1000 --year=2026 --from=10 --to=10 --status=CLOSED --accounts=1200-1299 --reason="Mahnlauf"
console ledger:periods --company=1000 --year=2026 --from=13 --to=16 --status=OPEN --accounts=2800-2999 --reason="Abschluss"
```

### Berechtigungen beim Buchen (Periode, Belegart)

Zusätzlich zu `JournalEntry.post` im Buchungskreis prüft der `PostingService` bei jedem
Buchungsweg (Fachmodul, Vorerfassung, Simulation, Storno) zwei Berechtigungen bis auf
Feldwerte (`internal/ledger/authz.go`, `sdk.Authorize`):

| Berechtigung | Berechtigungsfelder | Beispiel in der Rolle |
|---|---|---|
| `FiscalPeriod.post` – in der Periode buchen | `ledger`, `fiscal_year`, `posting_period` | alle: `posting_period` 1 – 12; Abschluss-Team zusätzlich 13 – 16 |
| `DocumentType.post` – Belege der Belegart buchen | `code` | Kreditorenbuchhaltung: `code` KR, KG; Hauptbuch: `code` SA, AB |

- Gepflegt wird in **Administration → Rollen → Berechtigungen** (Auswahl aus dem Catalog,
  Feldwerte als Unterzeilen). Beide Objects deklarieren ihre Felder über
  `crud.Entity.Authorization`.
- Rollen ohne diese Zeilen dürfen **nicht mehr buchen**. Für das bisherige Verhalten
  `FiscalPeriod.post` und `DocumentType.post` ohne Feldwerte geben (Administrator mit
  `*.*` hat sie schon).
- **Sehen** (crud.Access, seit 0.5.0): Belege, Einzelposten, Vorerfassungen samt Positionen und
  Salden nur mit `JournalEntry.read` im Buchungskreis; Sachkonten im Buchungskreis, Perioden,
  Kontensperren und die Buchungskreis-Steuerung mit ihrem eigenen `read`. Das Modul-Mapping
  der Steuerung ist eine Feldgruppe (`LedgerCompanyConfig.readFields`/`changeFields`, `mapping`).
  Bisher galt dafür `list`/`get` – Rollen mit eingeschränkten Buchungskreisen brauchen jetzt `read`.
- **Vorerfassung:** Das Feld **Sonderperiode** erscheint nur bei Buchungsdatum im
  Dezember und bietet nur die Perioden 13–16 an, in denen der Benutzer buchen darf. Für
  eine nicht erlaubte Belegart zeigt die Maske einen Hinweis.

### Offizielle Kontenrahmen importieren

`ledger:load-coa --file=…` versteht die Spalten offizieller Exporte. Beispiele sind DATEV
`Konto`/`Kontonummer` und `Beschriftung`/`Bezeichnung`, außerdem `Kontoart`,
`Kontengruppe`, `Feldstatusgruppe`, `Abstimmkonto` und `Steuerkategorie`.

- **Kontoart:** Fehlt sie, gilt die Kontenklasse (erste Ziffer) laut `class_types` des
  mitgelieferten Kontenrahmens. SKR04: 0–3 und 9 Bilanz, 4 Erlös, 5–6 Aufwand, 7 neutral.
- **Nummern:** Von Excel abgeschnittene führende Nullen werden auf `account_length` Stellen
  ergänzt.

### SystemEvents (Bewegungsdaten)

Das Hauptbuch meldet jede Änderung an Bewegungsdaten an den Event-Dispatcher des Kerns
(Object `SystemEvent`, siehe `../coremesh/internal/coreplugins/event`):

| Event | Wann | Inhalt (`data`) |
|---|---|---|
| `JournalEntry.post` | Beleg gebucht (Modul oder Vorerfassung), nicht bei Duplikaten | Belegnummer, Jahr, Periode, Herkunft, Referenz, Vorerfassung |
| `JournalEntry.reverse` | Stornobeleg gebucht | wie oben, dazu `reversed_document_id` |
| `JournalDraft.create/update/deactivate/post` | Vorerfassung angelegt, geändert, verworfen, gebucht | id, beim Buchen Beleg und Belegnummer |
| `JournalDraftItem.create/update/remove` | Position der Vorerfassung | id |

Gemeldet wird nach dem Commit, fehlgeschlagene Buchungen erzeugen kein Event. Andere
Plugins abonnieren z. B. `{"object": "JournalEntry", "action": "post", "company_code": "1000",
"callback": "RentContract"}` und erhalten die Events an `RentContract.onEvent`.

### Weitere Services

- `AccountBalance.list` – Summen- und Saldenliste aus dem Universal Journal (Hauswährung):
  - je Buchungskreis, Konto und Ledger,
  - Filter nach Jahr, Perioden, Datum und **jeder Kontierungsspalte**, z. B.
    `{"company_code": "1000", "rent_object_id": "WE-0001-0003"}`.
- `CurrencyConversion.convert` – `{"amount": "100", "from": "CHF", "to": "EUR", "date": "2026-10-01"}`.
  Kurse gelten direkt oder als Kehrwert, auch mit Faktoren (100 JPY = 0.6250 EUR).

### Tests

`go test ./...` läuft gegen SQLite mit echten Atlas-Migrationen und Fremdschlüsseln. Abgedeckt:
- Beträge und Rundung,
- idempotentes Laden von Kontenrahmen und Kursen,
- Einrichtung eines Buchungskreises,
- Modulbuchung mit Mapping, Idempotenz, allen Ablehnungsgründen und eigenem Mapping,
- Fremdwährung mit Rundungsdifferenz,
- Vorerfassung (speichern, prüfen, buchen, gesperrt, verwerfen),
- Storno, Salden je Mietobjekt, Rechte je Buchungskreis, Perioden und Sonderperioden,
- DDL-Gleichlauf, Metamodell, Konsolenbefehle und Übersetzungen (de, en, zh-CN).

### Nächste Schritte

- Parallele Ledger (2L) automatisch mitbuchen.
- Bilanz und Erfolgsrechnung aus den Salden.
- Offene Posten und Ausgleich (Debitoren/Kreditoren).
- Steuerkennzeichen und Steuerberechnung.
- Plugin `rent` (Mietverwaltung), das über `ledgerapi` bucht.
