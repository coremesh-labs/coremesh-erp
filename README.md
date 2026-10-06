# CoreMesh ERP

Fachplugins auf Basis von [CoreMesh](../coremesh) für

- **Mietverwaltung** (Liegenschaften, Mietobjekte, Mietverträge, Nebenkosten),
- **private Vertragsverwaltung** (Versicherungen, Darlehen, Abos …),
- **Buchhaltung** (Finanzbuchhaltung, später Debitoren/Kreditoren).

Go-Modul: `github.com/camel/coremesh_erp`. Die Plugins nutzen ausschließlich die öffentliche
API des Kerns (`github.com/camel/coremesh/pkg/sdk/...`). `go.mod` verweist per `replace` auf das
Nachbarverzeichnis `../coremesh`.

```
C:\ext-git\
├── coremesh\        Kern: Host, Core-Plugins (iam, catalog, dbschema), WebServer, Partner, Tags
└── coremesh-erp\    dieses Repository
    ├── cmd/plugins/<plugin>/main.go              ein Plugin = ein Prozess
    ├── cmd/plugins/<plugin>/internal/<modul>/    Fachcode (von außen nicht importierbar)
    ├── configs/                                  Host-Konfiguration der ERP-Plugins
    └── bin/plugins/<xx>/<name>-<version>-<os>-<arch>[.exe]
```

## Plugins

| Plugin | Modul (URL) | Inhalt | Version |
|---|---|---|---|
| `ledger` | `accounting` (`/m/accounting`) | Finanzbuchhaltung: Kontenplan, Buchungen (doppelte Buchführung), Storno, Salden | 0.1.0 |

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
`../coremesh-erp/bin/plugins`. Der Pfad ist relativ zum Arbeitsverzeichnis des Hosts. Die
Binaries bleiben damit in diesem Repository.

## Finanzbuchhaltung (`ledger` / `accounting`)

### Objects

| Object | Tabelle | Lebenszyklus | Inhalt |
|---|---|---|---|
| `GLAccount` (Kontenplan) | `ledger__accounts` | Status `ACTIVE` → `LOCKED` | Kontonummer, Bezeichnung, Kontoart (Aktiven, Passiven, Eigenkapital, Ertrag, Aufwand) |
| `JournalEntry` (Buchungen) | `ledger__journal_entries` | unveränderlich | Beleg: Belegnummer, Buchungskreis, Buchungs- und Belegdatum, Text, Referenz, Währung, Storno-Verweise |
| `JournalLine` (Positionen) | `ledger__journal_lines` | unveränderlich | Konto, Seite (Soll/Haben), Betrag |
| `AccountBalance` | – (Service, nur JSON-API) | – | Summen- und Saldenliste |

Der Kontenplan startet mit einem Grundkontenplan nach dem Schweizer KMU-Kontenrahmen,
vereinfacht für Liegenschaften und Privathaushalt. Beispiele: 1020 Bank, 1100 Forderungen
gegenüber Mietern, 2030 Mieterkautionen, 2400 Hypotheken, 3400 Mietertrag, 6100 Unterhalt,
6900 Hypothekarzinsen. Konten mit Buchungen werden gesperrt, nicht gelöscht.

### Grundsätze

- **Doppelte Buchführung:** Jeder Beleg hat mindestens zwei Positionen, Summe Soll = Summe Haben.
  Konten müssen existieren und aktiv sein.
- **Unveränderlich:** Belege haben weder `update` noch `delete`. Korrekturen laufen über
  **Storno** (`reverse`): Ein neuer Beleg bucht dieselben Positionen mit vertauschten Seiten.
  Die beiden Belege verweisen aufeinander (`reversal_of` / `reversed_by`).
  - Ein Beleg wird höchstens einmal storniert.
  - Ein Storno wird nicht storniert; stattdessen neu buchen.
  - Das Storno-Datum liegt nicht vor dem Originalbeleg.
- **Belegnummer** `<Jahr>-<laufende Nummer>` je Buchungskreis, z. B. `2026-000001`.
- **Beträge** werden exakt als ganze Rappen/Cent gespeichert (`amount_minor`). Eingabe
  `1500`, `1'500.50` oder `1500,50`; höchstens 2 Nachkommastellen.
- **Buchungskreise** (aus `iam`):
  - Buchen braucht `JournalEntry.post` im Buchungskreis des Belegs.
  - Stornieren braucht `JournalEntry.reverse`.
  - Listen, Positionen und Salden zeigen nur die Buchungskreise mit `JournalEntry.list`.
- **Protokoll:** `posted_at` und `posted_by` für jeden Beleg.

### API

```bash
# Einfache Buchung „Soll an Haben“ (so arbeitet auch das Formular „Buchen …“)
POST /api/v1/accounting/JournalEntry/post
{"data": {"company_code": "1000", "posting_date": "2026-10-01", "currency": "CHF",
          "text": "Miete Oktober", "debit_account": "1020", "credit_account": "3400", "amount": "1850.00"}}

# Sammelbuchung
{"data": {"company_code": "1000", "currency": "CHF", "text": "Miete und Akonto", "lines": [
  {"account_code": "1020", "debit": "2100"},
  {"account_code": "3400", "credit": "1850"},
  {"account_code": "3410", "credit": "250", "text": "Akonto Nebenkosten"}]}}

# Storno (Standardtext „Storno <Belegnummer>“, Datum heute)
POST /api/v1/accounting/JournalEntry/reverse
{"id": "<Beleg-ID>", "data": {"posting_date": "2026-10-07", "text": "Storno: falsches Konto"}}

# Summen- und Saldenliste (Saldo = Soll − Haben, positiv = Soll-Saldo)
POST /api/v1/accounting/AccountBalance/list
{"company_code": "1000", "date_from": "2026-01-01", "date_to": "2026-12-31"}
```

### Oberfläche

- **Kontenplan:** pflegen und sperren.
- **Buchungen:**
  - Liste mit Belegsumme.
  - „Buchen …“ (Formular Soll an Haben mit Kontenauswahl).
  - Detailansicht mit Positionen und Protokoll.
  - „Stornieren …“ in der Detailansicht.
- **Verweise:** Konten, Buchungskreis und Storno-Verweise haben das Kopfdaten-Symbol ⓘ des
  WebServers.

### Nächste Schritte

- Geschäftsjahre und Periodensperren (keine Buchungen in abgeschlossene Perioden).
- Bilanz und Erfolgsrechnung aus den Salden (Kontoart).
- Offene Posten: Debitoren (Mieter) und Kreditoren, Zahlungsabgleich.
- Mehrwertsteuer (Steuercodes, Abrechnung).
- Anbindung Mietverwaltung: Sollstellung der Mieten als Buchung, Mietobjekt als Kontierungsmerkmal
  (z. B. über das TagManagement mit einem Verweis-Tag).
