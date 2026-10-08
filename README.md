# CoreMesh ERP

Fachplugins auf Basis von [CoreMesh](../coremesh) für

- **Mietverwaltung** (Liegenschaften, Mietobjekte, Mietverträge, Nebenkosten),
- **private Vertragsverwaltung** (Versicherungen, Darlehen, Abos …),
- **Buchhaltung** (Hauptbuch, später Debitoren/Kreditoren, Anlagen).

Go-Modul: `github.com/coremesh-labs/coremesh-erp`. Die Plugins nutzen ausschließlich die öffentliche
API des Kerns (`github.com/coremesh-labs/coremesh/pkg/sdk/...`). `go.mod` verweist per `replace` auf das
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
| `ledger` | `ledger` (`/m/ledger`, `console ledger:…`) | Hauptbuch nach S/4HANA-Vorbild: Kontenpläne (SKA1/SKB1), Universal Journal (BKPF/ACDOCA), Vorerfassung, Periodensperre, Währungen und Tageskurse | 0.13.3 |
| `realestate` | `realestate` (`/m/realestate`, `console realestate:…`) | Immobilien: Wirtschaftseinheiten, Gebäude, Mietobjekte (Einheiten, Flächen, Pools, Vertragsobjekte), Bemessungen, Partner in Rollen, Kataloge je Buchungskreis | 0.3.1 |
| `contract` | `contract` (`/m/contract`, `console contract:…`) | Verträge: Mietverträge, Hausgeld, Dienstleistungs-, Versicherungs- und sonstige Verträge mit Partnern, Objekten, Konditionen (Haupt-/Nebenforderung, Sachkonto), Kündigung und Läufe und Sollstellungen | 0.7.0 |
| `procurement` | `procurement` (`/m/procurement`, `console procurement:…`) | Beschaffung: Angebote und Eingangsrechnungen von Handwerkern und Dienstleistern, Buchung über die Vorerfassung | 0.2.0 |
| `opcost` | `opcost` (`/m/opcost`, `console opcost:…`) | Betriebskosten: Kostenarten (umlagefähig nach BetrKV, nicht umlagefähig, Rücklagenzuführung) und Verteilerschlüssel | 0.1.0 |

## Bauen, testen, starten

Unter Windows ohne `make`:

```powershell
.\build.ps1          # baut alle Plugins nach bin/plugins
.\build.ps1 -Test    # go vet + go test
.\build.ps1 -Run     # bauen und den Host des Kerns mit beiden Konfigurationen starten
```

Unter Linux/macOS ohne `make`:

```bash
./build.sh           # baut alle Plugins nach bin/plugins
./build.sh --test    # go vet + go test
./build.sh --run     # bauen und den Host des Kerns mit beiden Konfigurationen starten
```

Mit `make` (alle Plattformen): `make build`, `make test`, `make run`. Vorher im Kern einmal
`make build` (Host, Console und Kern-Plugins). Die Binaries tragen `<os>-<arch>` im Namen
(z. B. `ledger-0.13.3-linux-amd64`), Windows- und Linux-Builds liegen also nebeneinander.

Erster Start unter Linux:

```bash
cd ../coremesh && make build
cd ../coremesh-erp && make run       # Web: http://localhost:8080, Konsole: ../coremesh/bin/console
```

Das Initialpasswort des Benutzers `admin` steht einmalig im Log („Erster Benutzer angelegt“).
Benötigt wird Go laut `go.mod` (bei älterem Go lädt `GOTOOLCHAIN=auto` die passende Version).

Der Host liest die Konfiguration des Kerns und dieses Repositories nacheinander:

```bash
cd ../coremesh
bin/host -config configs,../coremesh-erp/configs
```

`configs/10-ledger.yaml` trägt die Plugins ein und ergänzt `host.extra_plugin_dirs` um
`../coremesh-erp/bin/plugins`.

---

## Immobilien (`realestate`)

Verwaltung der Mietobjekte nach dem Vorbild von SAP RE-FX. Oberfläche: **Immobilien**
(`/m/realestate`), Konsole `console realestate:…`.

### Datenmodell

```
Wirtschaftseinheit (BusinessEntity)            LpzBrn
└─ Gebäude (Building)                          LpzBrn1
   └─ Mietobjekt (RentObject), Art kind:
      ├─ Mieteinheit (UNIT)                     LpzBrn1WG001   z. B. WEG-Wohnung, Stellplatz
      ├─ Fläche (SPACE)                         LpzBrn1LG001   optional aus einem Pool geschnitten
      ├─ Pool (POOL)                            LpzBrn1PL001   Gesamtfläche, aus der Flächen entstehen
      └─ Vertragsobjekt (COMPOSITE)             LpzBrn1WG003   Gegenstand des Mietvertrags
         └─ Bestandteile (CompositeItem, Zeitscheibe): Einheiten, Stellplätze, Flächen
Bemessungen (Measurement, Zeitscheibe) für jede Ebene: Wohnfläche, Nutzfläche, MEA, Zimmer …
Partner (RentObjectPartner, Zeitscheibe) für jede Ebene: Eigentümer, Hausmeister, Verwalter …
```

| Tabelle | Object | Inhalt |
|---|---|---|
| `realestate__business_entity` | `BusinessEntity` | Wirtschaftseinheit: Art, Adresse, Status, Gültigkeit |
| `realestate__building` | `Building` | Gebäude: Gebäudeart, Adresse (Vorschlag aus der Wirtschaftseinheit), Baujahr |
| `realestate__rent_object` | `RentObject` | **eine** Maske für alle Mietobjekte; Art `kind`, Nutzungsart, Geschoss, Lage, Pool |
| `realestate__composite_item` | `CompositeItem` | Bestandteile eines Vertragsobjekts mit Zeitscheibe |
| `realestate__measurement` | `Measurement` | Bemessungen mit Zeitscheibe, Maßeinheit (Standard aus der Bemessungsart) |
| `realestate__partner_role` | `RentPartnerRole` | Rolle aus dem Partnermodul, je Buchungskreis aktiviert: Ebenen, exklusiv, Anteil, Vererbung |
| `realestate__object_partner` | `RentObjectPartner` | Partner in einer Rolle an Wirtschaftseinheit, Gebäude oder Mietobjekt, Zeitscheibe, Anteil |
| `realestate__usage_type` … `location` | `UsageType`, `MeasurementType`, `MeasureUnit`, `ObjectStatus`, `EntityType`, `BuildingType`, `Floor`, `Location` | Kataloge **je Buchungskreis** |

- **Eine Maske:** Verträge, Kontierung im Hauptbuch (`rent_object_id`), Bemessungen, Partner
  und Tags verweisen auf eine Objekt-ID – unabhängig von der Art. Die Art wird beim Anlegen
  zuerst gewählt und ist danach fest. Welche Felder und Abschnitte je Art erscheinen,
  steuern **Darstellungsregeln** (Administration → Darstellung): `setup-company` legt je Art
  eine Regel an („Mietobjekt: Pool“ …), die der Administrator frei anpassen kann. Felder,
  die zur Art nicht passen, leert die Prüfung ohnehin.
- **Gültigkeit** ist ein Zeitraum (gültig ab/bis) mit Status aus dem Katalog – ohne
  Versionierung. **Zeitscheiben** gibt es nur, wo sich Werte über die Zeit ändern:
  Bemessungen (z. B. nach Umbau) und Bestandteile eines Vertragsobjekts.

### Sprechende IDs

| Ebene | Aufbau | Beispiel |
|---|---|---|
| Wirtschaftseinheit | frei, 2–30 Buchstaben/Ziffern | `LpzBrn` |
| Gebäude | Wirtschaftseinheit + Nummer | `LpzBrn1`, `LpzBrn2` |
| Mietobjekt | Gebäude + Kürzel der Nutzungsart + 3-stellige Nummer | `LpzBrn1WG001`, `LpzBrn1SP001` |

- Ohne Eingabe vergibt das Modul die nächste freie ID; eine eingegebene ID muss mit der ID der
  übergeordneten Ebene beginnen (Groß-/Kleinschreibung wird übernommen).
- Das Kürzel kommt aus der **Nutzungsart** des Buchungskreises (Wohnen → `WG`, Stellplatz →
  `SP` …); es ist je Buchungskreis eindeutig und frei wählbar.
- IDs sind systemweit eindeutig (über alle Ebenen und Buchungskreise).

### Regeln

- Nutzungsarten gelten für bestimmte Objektarten (z. B. `POOL` nur für Pools).
- **Vertragsobjekt:** enthält Mieteinheiten, Stellplätze und Flächen (keine Pools oder
  Vertragsobjekte); ein Objekt gehört zu einem Zeitpunkt zu **höchstens einem**
  Vertragsobjekt. Eine Zuordnung endet mit „Beenden …“.
- **Pool:** Die aus ihm geschnittenen Flächen ergeben zum Stichtag zusammen höchstens seine
  Gesamtfläche (in seiner Flächenart, z. B. Nutzfläche) – geprüft beim Erfassen der
  Bemessungen, beim Zuordnen von Flächen und beim Ändern der Gesamtfläche.

### Kataloge je Buchungskreis

Jeder Buchungskreis prägt seine Kataloge selbst aus. Beim Anlegen der ersten
Wirtschaftseinheit entstehen Vorschlagswerte (Nutzungsarten Wohnen, Gewerbe, Büro, Lager,
Stellplatz, Garage, Keller, Freifläche, Pool; Bemessungsarten Wohnfläche, Nutzfläche,
Heizfläche, MEA, Zimmer …; Maßeinheiten, Status, Arten, Geschosse, Lagen). Fehlende
Vorschlagswerte ergänzt auch:

```bash
console realestate:setup-company --company=1000
```

In der Oberfläche: Wirtschaftseinheiten → „Buchungskreis einrichten …“.

`setup-company` aktiviert außerdem die vorgeschlagenen Partnerrollen (sofern das Partnermodul
sie kennt) und legt die Darstellungsregeln der Mietobjekt-Maske an – jeweils nur Fehlendes.

### Geschäftspartner in Rollen

- **Rollen pflegt das Partnermodul** (Geschäftspartner → Kataloge → Rollentypen), dort hat
  auch jeder Partner seine Rollen mit Zeitraum. Vorschlag: `OWNER` Eigentümer, `JANITOR`
  Hausmeister, `WEGADM` WEG-Verwalter, `SEADM` Verwalter Sondereigentum.
- **Aktivierung je Buchungskreis** (Immobilien → Einstellungen → Partnerrollen) mit den
  Eigenschaften, die nur hier zählen: erlaubte **Ebenen** (z. B. Hausmeister nur an
  Wirtschaftseinheit und Gebäude), **exklusiv** (ein Partner je Stichtag), **Anteil** in %
  (Summe je Stichtag ≤ 100, z. B. Eigentümergemeinschaft), **Vererbung** an untergeordnete
  Objekte; die Bezeichnung lässt sich überschreiben.
- **Zuordnung** im Abschnitt „Partner“ von Wirtschaftseinheit, Gebäude und Mietobjekt, mit
  Zeitscheibe. Die Auswahl zeigt nur Partner, die die Rolle im Partnermodul haben; der
  Partner muss sie für den ganzen Zeitraum haben.
- **Wirksame Partner:** Was am Objekt fehlt, kommt vom Gebäude, dann von der
  Wirtschaftseinheit (tiefere Ebene übersteuert). Aktion „Wirksame Partner“ in der
  Detailansicht, `console realestate:partners --company=1000 --object=LpzBrn1WG001
  [--date=2026-06-01]`, für andere Plugins `RealEstateSetup.partners`.
- **Mieter** gehören nicht hierher – sie ergeben sich aus dem Mietvertrag. Die Vertragsverwaltung ergänzt
  sie über den Hook `realestate.partners` (Phase modify) in „Wirksame Partner“.
- Eigenes Recht `RentObjectPartner` (lesen/ändern je Buchungskreis): Wer Hausmeister pflegt,
  sieht nicht automatisch die Eigentümer.

### Merkmale (Tags)

Wirtschaftseinheit, Gebäude und Mietobjekt haben einen Abschnitt „Merkmale“ (Plugin `tag`).
Tag Sets werden dem Object `RentObject` (bzw. `Building`, `BusinessEntity`) zugewiesen und
lassen sich über die Bedingung auf `kind` oder `usage_type` einschränken, z. B. Ausstattung
nur für Wohnungen.

### Sichtbarkeit und Anbindung

- Alle Daten sind **je Buchungskreis** sichtbar: Recht `RentObject.read` mit den
  Berechtigungsfeldern `entity_id` und `building_id` – z. B. sieht ein Verwalter nur „seine“
  Gebäude. Kataloge haben ihr eigenes `read`.
- **Hauptbuch:** Das Modul abonniert `ledger.posting` (Phase check). Eine Kontierung
  `rent_object_id` muss auf ein Mietobjekt des Buchungskreises zeigen, das am Buchungsdatum
  gültig ist (Meldungen `RE-001`, `RE-002`). Im Hauptbuch ist das Feld „Mietobjekt“ ein
  Nachschlagefeld auf `RentObject`.
- **SystemEvents** bei jeder Änderung an Wirtschaftseinheiten, Gebäuden, Mietobjekten,
  Bestandteilen, Bemessungen und Partnerzuordnungen. Mietobjekte melden sich einheitlich als
  `RentObject` mit Art, Nutzungsart, Gebäude und Wirtschaftseinheit in den Daten.

## Verträge (`contract`)

Allgemeine Vertragsverwaltung in Anlehnung an SAP RE-FX / RE-CN: Mietverträge, Hausgeld,
WEG-Verwalter-, Dienstleistungs-, Versicherungs-, Versorgungs- und sonstige Verträge.
Oberfläche: **Verträge** (`/m/contract`), Konsole `console contract:…`. Verträge, Partner,
Objekte und Konditionen sind entkoppelt und haben Zeitscheiben.

### Datenmodell

| Tabelle | Object | Inhalt |
|---|---|---|
| `contract__contract` | `Contract` | Vertragskopf: interne und externe Nummer, Vertragsart, Vertragspartner, Richtung, Währung, Laufzeit, Status, Kündigung |
| `contract__partner` | `ContractPartner` | Partner in Rollen (Hauptmieter, Mitmieter, Bürge, Zahler …), Zeitscheibe, Anteil |
| `contract__object` | `ContractObject` | Objekte (Mietobjekt, Gebäude, Wirtschaftseinheit), Hauptobjekt, Zeitscheibe |
| `contract__condition` | `ContractCondition` | Konditionen: Betrag (Cent), fest oder je Einheit einer Bemessung, Rhythmus, Fälligkeit, Zahlungsweise, Sachkonto, abweichender Zahler, Zeitscheibe |
| `contract__notice_term` | `ContractNoticeTerm` | Kündigungsfrist, Stichtag, Mindestlaufzeit, Verlängerungsoption, Zeitscheibe |
| `contract__contract_type` | `ContractType` | Vertragsart je Buchungskreis: Richtung, Rolle des Vertragspartners, Nummernkreis, Objektpflicht, erlaubte Objekte, exklusive Objektnutzung |
| `contract__condition_type` | `ConditionType` | Konditionsart je Buchungskreis: **Haupt- oder Nebenforderung**, Vorauszahlung, Verrechnungsreihenfolge, Steuerkennzeichen |
| `contract__partner_role` | `ContractPartnerRole` | Rolle aus dem Partnermodul, je Buchungskreis aktiviert (exklusiv, Anteil) |
| `contract__account` | `ContractAccount` | Kontenfindung: zulässige Sachkonten je Vertragsart und Konditionsart, Standardkonto |

### Regeln

- **Richtung:** Forderung (wir erhalten, z. B. Miete, Hausgeld) oder Verbindlichkeit (wir
  zahlen, z. B. Versicherung, Strom, Wartung) – aus der Vertragsart.
- **Nummern:** Die interne Vertragsnummer kommt aus dem Nummernkreis `Contract` (Core-Plugin
  `numrange`), Intervallschlüssel = Nummernkreis der Vertragsart, z. B. `MV-2026-0001`. Die
  **externe Vertragsnummer** ist Pflicht; ohne Eingabe entsteht
  `<Vertragsart>-<3 Buchstaben des Partners>-<nnn>` (z. B. `MV-MUE-001`, Umlaute
  ausgeschrieben), die laufende Nummer aus dem Nummernkreis `ContractExternal`.
- **Vertragspartner** ist Pflicht und kommt aus dem Partnermodul: Die Auswahl zeigt nur Partner
  mit der Rolle der Vertragsart (z. B. Mieter). Er steht danach für die Laufzeit im Abschnitt
  Partner; weitere Partner und Wechsel dort mit Zeitscheibe.
- **Status:** Entwurf → **Aktivieren** (Prüfung: Hauptrolle lückenlos besetzt, Objekt, wenn die
  Vertragsart es verlangt, mindestens eine Kondition; Hook `contract.activate`) →
  **Kündigen …** (Eingang, von wem, Grund, Ende). Das früheste Ende ergibt sich aus den
  Kündigungsregeln: Monatsende nach der Frist, Eingang nach dem Stichtag zählt ab dem
  Folgemonat, nicht vor Ablauf der Mindestlaufzeit; eine Aufhebung („einvernehmlich“) darf
  früher enden. Partner, Objekte und Konditionen enden mit dem Vertrag.
- **Leerstand:** Bei Vertragsarten mit exklusiver Objektnutzung gehört ein Objekt zu einem
  Zeitpunkt höchstens einem Vertrag – auch über Vertragsobjekte der Immobilienverwaltung
  (ein vermietetes Vertragsobjekt sperrt seine Bestandteile und umgekehrt).
- **Konditionen:** Betrag in der kleinsten Einheit der Vertragswährung (Eingabe `1.250,50`),
  fest oder je Einheit einer Bemessung (z. B. € je m² Wohnfläche des Objekts). Das Sachkonto
  ist eine Auswahl aus der **Kontenfindung** der Vertragsart und Konditionsart; leer = das
  Standardkonto. Fälligkeit: Rhythmus, Fälligkeitstag (z. B. der 15., wenn der Mieter zur
  Monatsmitte zahlt), vor- oder nachschüssig; **einmalige** Konditionen (Bereitstellungsgebühr)
  mit eigenem Fälligkeitsdatum (leer = Beginn). Minderungen sind Konditionen mit negativem
  Betrag, z. B. Konditionsart `MM`. Gerechnet und gebucht wird im Plugin `contract-billing`.
- **Haupt- und Nebenforderung** wie in SAP: Hauptforderungen (Miete, Hausgeld, Prämie),
  Nebenforderungen (Mahngebühren, Verzugszinsen, Kosten) mit Verrechnungsreihenfolge
  (§ 367 BGB: Kosten, Zinsen, Hauptleistung).

### Einrichtung je Buchungskreis

```bash
console contract:setup-company --company=1000
```

oder Verträge → Vertragsarten → „Buchungskreis einrichten …“. Legt fehlende Vorschlagswerte
an: Partnerrollen (sofern das Partnermodul sie kennt: Mieter, Vermieter, Eigentümer, Kreditor,
Debitor, WEG-Verwalter, Hausmeister, Bürge, Zahler), Vertragsarten (MV Wohnraummiete, GM
Gewerbemiete, SP Stellplatz, HG Hausgeld, VV WEG-Verwaltervertrag, DL Dienstleistung, VS
Versicherung, VE Versorgung, SO Sonstiges – nur mit aktivierter Rolle) und Konditionsarten (KM
Kaltmiete, NK/HK Vorauszahlungen, ST Stellplatz, HG Hausgeld, EN Entgelt/Prämie, MG
Mahngebühr, ZI Verzugszinsen). Die Kontenfindung pflegt der Buchungskreis selbst.

### Anbindung

- **Immobilien:** Das Modul abonniert `realestate.partners` (Phase modify): „Wirksame
  Partner“ eines Mietobjekts zeigt auch die Partner der Verträge (aktiv oder gekündigt), z. B.
  den Mieter – direkt oder über ein Vertragsobjekt, zu dem das Objekt gehört.
- **Hauptbuch:** Die Kontierung „Mietvertrag“ (`rent_contract_id`) ist eine Auswahl auf
  `Contract`; Sachkonten der Kontenfindung prüft das Modul über `GLAccountCompany`.
- **Rechte:** `Contract.read` mit dem Feld `contract_type` (z. B. nur Versicherungen),
  Kataloge mit eigenem Recht; SystemEvents bei jeder Änderung und bei Statuswechseln.
- **Merkmale (Tags)** im Vertrag, Tag Sets über die Bedingung auf `contract_type`.
- **Kontonummern** der Kontenfindung und der Konditionen speichert das Modul so, wie das
  Hauptbuch sie führt (mit Kontenplan-Präfix, `SKR25-6000`); Eingaben ohne Präfix passen.

### Sollstellung (Plugin `contract-billing`)

Gerechnet und gebucht wird vom Haskell-Plugin `contract-billing` ([coremesh-erph](../coremesh-erph)),
das selbst keine Daten hält. Dieses Modul hält Läufe und Sollstellungen und stellt die Oberfläche:

| Tabelle | Object | Inhalt |
|---|---|---|
| `contract__posting_run` | `ContractPostingRun` | Buchungslauf: Buchungskreis, Stichtag, Status, Belege, davon vorerfasst, Nachberechnungen, Meldungen |
| `contract__posting` | `ContractPosting` | Vermerk je Kondition und Zeitraum: **Sollstellung** (zur Fälligkeit) oder **Nachberechnung** (Differenz zum Lauftag), laufende Nummer (0 = erster Vermerk), Kalenderperiode, Betrag (Cent), Status **vorerfasst** oder **gebucht**, Vorerfassung, Beleg, Lauf |

- **Buchung → Buchungsläufe:** „Buchungslauf …“ (Buchungskreis, Stichtag) bucht alles, was fällig
  und noch nicht vermerkt ist, und rechnet schon vermerkte Perioden nach; „Vorschau …“ plant
  nur. **„Buchen …“ am Vertrag** mit dem Feld „Buchen bis“ (nur im Aktionsformular). Die
  Formulare schlagen heute vor und zeigen den letzten Lauf bzw. die letzte Sollstellung des
  Vertrags. Der Lauf muss nicht regelmäßig laufen: Ausgelassene Monate werden mit ihrer
  Fälligkeit nachgeholt.
- **Nachberechnung:** Rückwirkende Änderungen (Minderung wegen Mängeln, Mieterhöhung, Korrektur
  eines Betrags, rückwirkende Kündigung) bucht der nächste Lauf als Differenz zum Lauftag –
  bei negativem Saldo als **Gutschrift** (Belegart DG bzw. KG).
- **Vertragsart, Gruppe „Buchung“:** **Partnerkonto im Buchungskreis Pflicht** (Standard an),
  Belegart (leer = DR bzw. KR), Belegart für Gutschriften (leer = DG bzw. KG), **automatisch ins
  Hauptbuch buchen** (Standard aus: die geprüfte Vorerfassung bleibt offen).
- **Kaution:** eigener Vertrag (Vertragsart `KT` Mietkaution) mit **Bezugsvertrag** (der
  Mietvertrag; erlaubte Arten an der Vertragsart, „Bezugsvertrag Pflicht“), Kondition `KA`
  einmalig mit **Monatsraten** (Standard der Konditionsart: 3, § 551 BGB). Die Kontenfindung
  KT/KA zeigt auf ein Bilanzkonto (z. B. 2850 Verbindlichkeiten aus Mietkautionen) – die
  Kaution berührt die GuV nicht. Am Mietvertrag: Abschnitt „Zugehörige Verträge“.
- **Darlehen:** Vertragsarten `DA` (aufgenommen, Partner Bank als Kreditor) und `DV`
  (vergeben, Debitor) mit **Darlehenskonditionen** (`ContractLoan`, Zeitscheiben z. B. je
  Zinsbindung): Betrag, Auszahlung bzw. Übernahme mit Anfangsbestand, Tilgungsart (Annuität,
  Ratentilgung, endfällig), Zinssatz, Rate, Rhythmus, Fälligkeitstag, Zinsmethode (30/360
  deutsch, act/360, act/365), Darlehens- und Zinskonto, Konditionsarten der Vermerke (DZ, DT,
  DS, AZ); **Sondertilgungen** (`ContractLoanPayment`). Tilgungsplan und Sollstellungen
  rechnet contract-billing; Aktion **„Tilgungsplan“** am Vertrag (Tabelle).
- **Vertragsabrechnung** (`ContractSettlement`, Positionen `ContractSettlementItem`): Der Partner
  rechnet die Vorauszahlungen eines Zeitraums ab – Versorger, Grundsteuerbescheid,
  **WEG-Jahresabrechnung** (Vertragsart `WH` „Hausgeld an WEG“, Konditionen `HV`/`RZ`
  als Vorauszahlung).
  - Kopf: Zeitraum, Datum, Nummer; **laut Abrechnung** Vorauszahlungen und Ergebnis;
    **Anteil Erhaltungsrücklage** (Anfang, Entnahme, Ende), Rücklagen- und Entnahmekonto.
  - Positionen je Kostenart (Modul Betriebskosten): **Gesamtkosten** (der WEG),
    **Verteilerschlüssel** mit Gesamt- und Anteilswert, **Einzelbetrag**, Sachkonto, Objekt,
    Abrechnungskreis (z. B. Tiefgarage).
  - **„Prüfen“** (contract-billing): Einzelbetrag = Gesamtkosten × Anteil / Gesamt;
    Vorauszahlungen laut Abrechnung = gebuchte Vorauszahlungen; Ergebnis laut Abrechnung;
    Rücklage (Anfang + Zuführung − Entnahme = Ende, Anfang = Vorjahr, Ende = Saldo im
    Hauptbuch); fehlende Kostenarten des Vorjahres. Toleranz an der Vertragsart.
  - **„Buchen …“**: Kosten je Position im Soll (Rücklagenzuführung aufs Bilanzkonto),
    gebuchte Vorauszahlungen im Haben, Entnahme, Differenz an das Partnerkonto
    (Nachzahlung bzw. Guthaben) – über die Vorerfassung.
  - **„Positionen aus dem Vorjahr übernehmen“**: Kostenarten, Schlüssel, Konten, Objekte –
    nur die Beträge fehlen.
  - Vorauszahlungen buchen auf ein **Bilanzkonto** (Kontenfindung der
    Vorauszahlungs-Konditionsart, z. B. 1400 noch nicht abgerechnete Betriebskosten).
- **Partnerverweise:** Vertrag, Vertragspartner und Zahler verweisen auf die **BP-Nummer**
  (Partnermodul ab 0.10.0). Verweise auf alte GUIDs stellen contract, realestate und ledger
  beim ersten Aufruf nach dem Update selbst um (`Migrate`, `BusinessPartnerService.resolve`) –
  im Hauptbuch auch Kunde/Lieferant gebuchter Positionen (technischer Schlüssel, der Partner
  bleibt derselbe).
- **Partnerkonto:** Das Abstimmkonto (z. B. das Mieterkonto) steht in den **Buchungskreisdaten
  des Partners in der Rolle der Vertragsart** (Partnermodul; jede Finanzrolle hat ihr eigenes
  Konto). Mit „Partnerkonto Pflicht“ lassen sich Vertrag, Vertragspartner in dieser Rolle und
  abweichender Zahler nur speichern bzw. der Vertrag nur aktivieren, wenn diese Daten im
  Buchungskreis des Vertrags vorhanden sind und das Konto im Hauptbuch ein Abstimmkonto passender
  Art ist (Debitor-Rolle → Debitoren, Kreditor-Rolle → Kreditoren). Die Rolle der Vertragsart muss
  dann eine Finanzrolle sein (z. B. Eigentümer für Hausgeld als Debitor einstellen). Das
  Abstimmkonto an der Vertragsart (bis 0.4.0) entfällt.
- **`ContractPostingService.record`** (RFC-artig, ohne Oberfläche): contract-billing vermerkt die
  Fälligkeiten eines Belegs – in seiner Transaktion, zusammen mit Vorerfassung und Buchung.
- **Abgleich mit dem Hauptbuch:** `ContractPostingService.onEvent` hört auf
  `JournalDraft.post` (Belegnummer übernehmen) und `JournalDraft.deactivate` (Fälligkeit wieder
  offen); vor jedem Lauf werden die offenen Vorerfassungen zusätzlich per `JournalDraft.get`
  abgeglichen.
- **Rechte:** `ContractPostingRun.execute`/`preview` bzw. `Contract.post` im Buchungskreis; Läufe
  und Sollstellungen mit `read` je Buchungskreis. Ohne laufendes Plugin `contract-billing` meldet
  der Lauf „nicht verfügbar“.
- **Events für die Kopie in contract-billing:** Vertragsart (neu), Vertrag, Kondition,
  Vertragsobjekt; im Hauptbuch (ab 0.12.0) Sachkonto im Buchungskreis und Feldstatus.

## Betriebskosten (`opcost`)

Stammdaten, die Vertragsabrechnung, Beschaffung und der Nebenkostenrechner gemeinsam nutzen.

| Tabelle | Object | Inhalt |
|---|---|---|
| `opcost__cost_category` | `CostCategory` | Kostenart: **Art** (umlagefähig nach § 2 BetrKV, nicht umlagefähig, Zuführung Erhaltungsrücklage), Vorschlag Sachkonto (bei der Rücklage das Bilanzkonto), Nr. nach BetrKV |
| `opcost__allocation_key` | `AllocationKey` | Verteilerschlüssel: Grundlage Bemessung der Immobilienverwaltung (MEA, Wohnfläche, Heizfläche …), Anzahl Einheiten, Personen, Verbrauch, direkt |

Einrichten: `console opcost:setup-company --company 1000` – die 17 Betriebskostenarten der BetrKV,
Verwaltung, Instandhaltung, Kontoführung, Rücklagenzuführung und die üblichen Schlüssel. Die
Sachkonten pflegt der Buchungskreis.

## Beschaffung (`procurement`)

Angebote und Eingangsrechnungen von Handwerkern und Dienstleistern. Beträge sind **brutto**
(Umsatzsteuer folgt), in der kleinsten Einheit der Währung.

| Tabelle | Object | Inhalt |
|---|---|---|
| `procurement__invoice_type` | `InvoiceType` | Rechnungsart je Buchungskreis: Belegart im Hauptbuch (Rechnung, Gutschrift), **Rolle des Lieferanten** (Finanzrolle mit Abstimmkonto), Nummernkreis, automatisch buchen |
| `procurement__object_posting` | `ObjectPosting` | Kontierung der Objekte: welches Feld im Hauptbuch eine Objektart bekommt (Vorschlag: Mietobjekt → `rent_object_id`, Gebäude → Dimension 1, Wirtschaftseinheit → Dimension 2) |
| `procurement__quote` | `PurchaseQuote` | Angebot `AN-<Jahr>-<n>`: Lieferant, Leistung, Betrag, gültig bis, Objekt, Kostenart; **Annehmen** / **Ablehnen** |
| `procurement__invoice` | `SupplierInvoice` | Eingangsrechnung `<Rechnungsart>-<Jahr>-<n>`: Lieferant, Rechnungsnummer des Lieferanten (je Lieferant eindeutig), Rechnungs-/Buchungsdatum, Fälligkeit, angenommenes Angebot, Objekt; Status erfasst → vorerfasst → gebucht bzw. storniert |
| `procurement__invoice_item` | `SupplierInvoiceItem` | Position: Kostenart (Modul Betriebskosten), Sachkonto (Vorschlag der Kostenart), Betrag (negativ = Gutschrift), Objekt (leer = Rechnung), Kostenstelle, **umlagefähig**, **Leistungszeitraum** |

- **„Buchen …“** an der Rechnung: Vorerfassung im Hauptbuch – je Position Aufwand im Soll,
  Gegenposition Haben auf das **Abstimmkonto des Lieferanten** (Buchungskreisdaten in der Rolle
  der Rechnungsart, Buchungssperre wird beachtet) mit dem Lieferanten als Partner; Kontierungen
  nur, wo der Feldstatus des Kontos sie zulässt; Belegart der Rechnungsart, bei negativem
  Saldo die Gutschrift-Belegart. Dann prüfen und bei „automatisch buchen“ buchen – alles in
  einer Transaktion: Scheitert die Prüfung, bleibt die Rechnung erfasst.
- Wird die Vorerfassung im Hauptbuch gebucht oder verworfen, folgt die Rechnung
  (SystemEvents `JournalDraft.post`/`deactivate`).
- **„Stornieren …“**: erfasst → storniert; vorerfasst → Vorerfassung verwerfen; gebucht →
  Storno im Hauptbuch zum angegebenen Datum.
- Positionen sind nur änderbar, solange die Rechnung erfasst ist; „Inaktivieren“ entfernt eine.
- **Einrichten:** `console procurement:setup-company --company 1000` bzw. „Buchungskreis
  einrichten …“ an den Rechnungsarten. Kostenarten seit 0.2.0 im Modul Betriebskosten.
- Regelmäßige Kosten mit Vorauszahlung (Grundsteuer, Wasser, Hausgeld) gehören nicht hierher,
  sondern als Vertrag mit **Vertragsabrechnung** ins Vertragsmodul.
- Die Positionen mit umlagefähig, Objekt und Leistungszeitraum sind die Grundlage des
  Nebenkostenrechners.

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
| `ledger__posting_period` | `PostingPeriod` | T009B | Periodendefinition 01–16 ohne Geschäftsjahr: Bezeichnung, Sonderperiode, Kalendermonat |
| `ledger__open_period` | `FiscalPeriod` | OB52 | **Liste der offenen Perioden** je Buchungskreis, Ledger, Jahr und Periode; was nicht (offen) darin steht, ist gesperrt. Schließen behält den Verlauf (geschlossen am/von) |
| `ledger__document_numbering` | `DocumentNumbering` | NRIV/T003 | **Belegnummernvergabe:** Intervall des Nummernkreises `JournalEntry` je Buchungskreis, Ledger und Belegart (`*` = alle übrigen) |
| `ledger__journal_header` | `JournalEntry` | BKPF | Belegkopf, Schlüssel **Buchungskreis, Geschäftsjahr, Belegnummer**: Periode, Daten, Belegart, Währungen, Kurs, Herkunft (Modul, Referenz), Storno |
| `ledger__journal_item` | `JournalEntryItem` | ACDOCA | Einzelposten, Schlüssel **Beleg, Ledger, Position**: Konto, Soll/Haben, Betrag Beleg-/Hauswährung, Kostenstelle, Profit-Center, Segment, SD-, RENT-, Einkaufs- und freie Dimensionen |
| `ledger__draft_header`, `ledger__draft_item` | `JournalDraft`, `JournalDraftItem` | VBKPF/VBSEG | Vorerfassung manueller Buchungen |
| `ledger__currency` | `Currency` | TCURC/TCURX | Währung mit Nachkommastellen |
| `ledger__exchange_rate` | `ExchangeRate` | TCURR | Tageskurs je Kurstyp (`M`, `B`, `G`) und Währungspaar ab Gültigkeitsdatum, mit Umrechnungsfaktoren |

**Beträge** stehen als ganze Zahl in der kleinsten Einheit der Währung (EUR: Cent, JPY: Yen),
vorzeichenbehaftet wie in ACDOCA: Soll positiv, Haben negativ. Summe eines Belegs je Ledger = 0.

**Fremdschlüssel, auch rekursiv:**
- Positionen → Kopf, Konto (Kontenplan + Nummer), Ledger.
- **Storno ↔ Original** als Selbstbezug des Belegkopfs (gleicher Buchungskreis):
  `reversed_fiscal_year`/`reversed_document_number` und
  `reversal_fiscal_year`/`reversal_document_number` → `journal_header`.
- **Vorerfassung ↔ Beleg:** `journal_header.draft_id` → Vorerfassung und
  `draft_header.posted_fiscal_year`/`posted_document_number` → Beleg.
- Die ID eines Belegs in Actions und Oberfläche ist der zusammengesetzte Schlüssel
  `<Buchungskreis>|<Jahr>|<Belegnummer>`, z. B. `1000|2026|1000000011`
  (`reversed_document_id`, `posted_document_id` usw. sind daraus abgeleitete Felder).

### Belegnummern

- Belegnummern kommen aus dem Nummernkreis **`JournalEntry`** (Core-Plugin `numrange`) je
  Buchungskreis und Geschäftsjahr. Der Nummernkreis ist
  - **lückenlos:** die Nummer wird in der Transaktion der Buchung gezogen; scheitert die
    Buchung, ist auch die Nummer nicht verbraucht,
  - **überschneidungsfrei:** Intervalle desselben Buchungskreises und Jahres dürfen sich nicht
    überlappen – eine Belegnummer ist im Buchungskreis und Jahr eindeutig.
- **Belegnummernvergabe** (Hauptbuch → Einstellungen): welches Intervall (Intervallschlüssel)
  je Buchungskreis, Ledger und Belegart gilt; Belegart `*` gilt für alle übrigen.
- `setup-company` und die Migration legen je Ledger einen Eintrag `*` an: führendes Ledger
  `0L` mit `1000000001–1999999999`, jedes weitere Ledger die nächste Milliarde
  (`2L`: `2000000001–2999999999`). Fehlt das Intervall für ein neues Jahr, legt `numrange`
  es nach dem Vorjahr an.
- **Positionen** sind je Beleg und Ledger fortlaufend nummeriert (1, 2, 3 …), angezeigt nach
  Soll vor Haben.
- **Migration (0.13.0):** Belege und Positionen mit GUID aus `ledger__journal_entry_header`/
  `_item` werden mit ihrer Belegnummer übernommen, der Stand aus `ledger__number_range` in den
  Nummernkreis; die alten Tabellen bleiben leer stehen (dbschema löscht keine Tabellen).
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
   - Geschäftsjahr und Periode ergeben sich aus dem Buchungsdatum (Variante K4): die normale
     Periode des Kalendermonats laut Periodendefinition.
   - Sonderperioden 13–16 nur mit Buchungsdatum im Dezember.
     In der Vorerfassung über das Feld „Sonderperiode“.
   - Die Periode muss in der Liste der offenen Perioden stehen (`ledger__open_period`).
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
| DG Debitorengutschrift | Debitor, Sachkonto, Steuer | ja |
| KR Kreditorenrechnung | Kreditor, Sachkonto, Steuer | ja |
| KZ Kreditorenzahlung | Kreditor, Sachkonto | nein |
| KG Kreditorengutschrift | Kreditor, Sachkonto, Steuer | ja |
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

### Kontonummern und Kontoarten

- **Kontonummer = <Kontenplan>-<Nummer>**, z. B. `SKR25-1200`. Ein Sachkonto gehört nur zu seinem
  Kontenplan; das Konto im Buchungskreis verweist darauf. Eingaben ohne Präfix (Formulare,
  Fachmodule, Dateien, Konsole) ergänzt der Ledger um den Kontenplan – im Kontenplan um dessen
  eigenen, im Buchungskreis um den des Buchungskreises. Ein fremder Kontenplan wird abgelehnt.
  Listenfilter finden Konten mit und ohne Präfix (`1200` findet `SKR25-1200`).
- **Kontoarten** (Einstellungen → Kontoarten, analog KOART): A Anlagen, D Debitoren, K Kreditoren,
  M Material, S Sachkonten, V Vertragskonten. Im Kontenplan ist ein Sachkonto S, ein
  Abstimmkonto D, K, A oder V. Jede Position speichert ihre Kontoart (aus der Positionsart:
  GL/Steuer → S, CUSTOMER → D, SUPPLIER → K, ASSET → A). Das Feld „Kontoart“ des Kontenplans
  aus früheren Versionen heißt jetzt **Kontotyp** (Bilanz, Aufwand, Erlös …).
- **Jahr/Periode:** Belegkopf und Einzelposten tragen zusätzlich `fiscal_year_period` (JJJJPPP,
  z. B. 2026010) für Auswertungen und Filter.
- **Übernahme aus 0.8.0:** Beim ersten Zugriff stellt der Ledger Kontonummern, Kontoarten,
  Jahr/Periode und die Periodendefinition je Buchungskreis einmalig um.

### Buchungsperioden öffnen und schließen

- **Periodendefinition** (Einstellungen) **je Buchungskreis**: Perioden 01–16 mit Bezeichnung,
  Kennzeichen Sonderperiode und Kalendermonat. Neue Buchungskreise erhalten die Vorlage 01–12 +
  13–16 (Dezember). Eine Sonderperiode gehört zu einem Monat (Standard: 13–16 im
  Dezember) und wird beim Buchen ausdrücklich angegeben.
- **Offene Buchungsperioden** (Einstellungen): die Liste je Buchungskreis und Ledger. Monatlich
  kommt eine Periode dazu, eine alte geht heraus; zum Jahreswechsel stehen einfach Perioden beider
  Jahre darin (z. B. 12/2025 und 1/2026).
  - „Perioden öffnen …“ / „Perioden schließen …“ für einen Bereich, „Schließen“ je Zeile.
  - Geschlossene Perioden verschwinden aus der Liste; mit „Inaktive anzeigen“ sieht man den
    Verlauf (geöffnet/geschlossen am, von). Erneutes Öffnen legt eine neue Zeile an.
- **Kontoart in den offenen Perioden:** `+` (Standard) ist der Hauptschalter und muss offen sein.
  Kontoarten mit **eigener Periodensteuerung** brauchen zusätzlich eine eigene offene Periode –
  z. B. D und K zum Monatsende schließen und S für Abschlussbuchungen offen lassen. Kontoarten
  ohne eigene Steuerung brauchen keine Zeilen.
  - Konsole: `console ledger:periods --company=1000 --year=2026 --from=11 --to=11 --status=OPEN`.
  - Je Kontoart: `--kind=D` (ohne Angabe `+`).
- **Übernahme aus 0.7.0:** Die offenen Perioden aus `ledger__fiscal_period_status` werden beim
  ersten Zugriff einmalig übernommen; die alte Tabelle bleibt unverändert stehen.

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

### Darstellungsregeln für Einzelposten

Einzelposten tragen seit 0.6.0 die Herkunft des Belegs (`source_module`, ältere werden beim
ersten Lesen ergänzt). Damit lässt sich unter **Administration → Darstellung** z. B. festlegen:
`JournalEntryItem`, Bedingung *Herkunft* = RENT → *Kundenauftrag (SD)* und
*Verkaufsorganisation (SD)* ausblenden. Den Kunden nicht ausblenden – bei RENT steht dort
der Mieter (Mapping `tenant` → `sd_customer_id`).

### Hook ledger.posting

Andere Module prüfen und ergänzen Buchungen über den Hook `ledger.posting` (Core-Plugin `hook`,
`pkg/sdk/hook`) – bei Fachmodul-Buchungen und Vorerfassung; `modify` und `check` auch beim Prüfen:

| Phase | Zeitpunkt | Data |
|---|---|---|
| `modify` | vor der Prüfung des Ledgers | `{request}` – ReturnData mit geändertem `request` ersetzt den Auftrag |
| `check` | nach der Prüfung, vor dem Schreiben | dazu `fiscal_year`, `posting_period`, `ledger`; Meldung E bricht ab |
| `commit` | nach dem Commit | dazu `result` (Belegnummer …); Fehler werden Warnungen |

Warnungen und Hinweise kommen in `PostResult.Messages` zurück und erscheinen in der
Meldung der Oberfläche. Übersicht und Sperren der Abos: **Erweiterungen → Hooks**.

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
- **Datenströme für Rechen- und Auswertungsmodule:** Jede Liste (crud) ist zusätzlich über
  `Read` abrufbar, z. B. alle Einzelposten eines Jahres ohne Seitengrenze:
  `env.Services.Read(ctx, "JournalEntryItem", "list", map[string]any{"company_code_id": "1000",
  "fiscal_year": 2026}, w)` bzw. `console --object JournalEntryItem --action list --read …`.
  Gleiche Filter und Leserechte wie `list`, Beträge als ganze Zahlen (Cent). Siehe
  `../coremesh/pkg/sdk/module/README.md`, Abschnitt „Datenströme“.
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
