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
    ├── docs/                                     DDL, Entwürfe, Buchungsleitfaden (ODT)
    └── bin/plugins/<xx>/<name>-<version>-<os>-<arch>[.exe]
```

## Plugins

| Plugin | Modul (URL, Konsole) | Inhalt | Version |
|---|---|---|---|
| `ledger` | `ledger` (`/m/ledger`, `console ledger:…`) | Hauptbuch nach S/4HANA-Vorbild: Kontenpläne (SKA1/SKB1), Universal Journal (BKPF/ACDOCA), Vorerfassung, Periodensperre, Währungen und Tageskurse, hierarchischer Kontenplan, rückwirkend buchen ab, Kontenplanwechsel, Steuerkennzeichen, Journal (PDF, CSV; Reihenfolge wählbar), Mengeneinheiten, Rundungsregeln (Standard je Buchungskreis), aufzuteilende Vorsteuer (Steuerkennzeichen VA19/VA7, Bemessungsart), Konto für nicht abziehbare Vorsteuer am Sachkonto, Gebäude und Wirtschaftseinheit an Bestandskonten, Bilanzschema als Katalog (Posten mit Kontenbereichen, Vorschlag SKR04; die Bilanz rechnet ein Bericht in „Skripte und Berichte“), Gliederungsschema für Überschussrechnungen (Art Ergebnis, Einnahmen/Werbungskosten, Ausweis je Konto, Mietobjekt oder Vertrag; Vorschlag Anlage V SKR04, Umlagen inkl. NK-Vorauszahlungen), offene Posten und Ausgleich auf Debitoren-/Kreditorenkonten (automatisch nach dem Buchen: Storno, passender Beleg, passender Betrag, älteste zuerst; von Hand am Einzelposten; Service OpenItem) | 0.29.5 |
| `realestate` | `realestate` (`/m/realestate`, `console realestate:…`) | Immobilien: Wirtschaftseinheiten, Gebäude, Mietobjekte (Einheiten, Flächen, Pools, Vertragsobjekte), Bemessungen, Partner in Rollen, Kataloge je Buchungskreis, Abschnitt Texte an Wirtschaftseinheit, Gebäude und Mietobjekt | 0.3.9 |
| `contract` | `contract` (`/m/contract`, `console contract:…`) | Verträge: Mietverträge, Hausgeld, Dienstleistungs-, Versicherungs- und sonstige Verträge mit Partnern, Objekten, Konditionen (Haupt-/Nebenforderung, Sachkonto), Kündigung und Läufe und Sollstellungen, Personen (Zeitscheiben), Bankkonten und Kreditkarten, Mieter-Merkmale, Sollstellung ab, Abschreibung, Wirtschaftspläne, Rahmenverträge mit Rechnungsvorlagen, Vertragsübersicht (PDF quer, Excel), Konditionen mit Menge × Einzelpreis und Rundungsregel, nachgelagerte Konditionen (Steuer, Prozent; Zuschlag oder Anteil), Positionen eines Vertrags, Tilgungsplan als PDF, Kalkulationsschema an der Kondition, Tilgungsplan bis Stichtag, Merkmale der Versorgungsverträge (Tag Set VERSORGUNG), Konditionsarten (Verhalten) und Konditionsgegenstände, Abrechnungsweg der Vertragsart (Sollstellung, Eingangsrechnung, beides), Kontenfindung je Konditionsart, Kondition nur mit gesichertem Sachkonto, Vorsteuerquote je Objekt (Anteil der Mietobjekte mit Umsatzsteuer), Abschnitt Texte am Vertrag, Position der Kondition (derselbe Gegenstand mehrfach), Vertragsart ändern (Konditionen mit Kontenfindung der neuen Art), Gegenstände der Kondition nach Kontenfindung der Vertragsart, je Einheit ohne Objekt = Bemessung des Hauptobjekts, Konditionen am Vertrag mit Menge, Einheit, Einzelpreis und Gesamtpreis, Zeitscheiben zusammenfassen, Vertragskonto und Darlehensstand (aus dem Hauptbuch über GuV), Objektauswahl nach Objektart, Anteil der Vertragspartner auch ohne Pflicht, Partner/Objekte/Konditionen ohne Ende enden mit dem Vertrag, Abschreibung in Raten im Jahr und mit festem Jahresbetrag, Vertragspartner Pflicht je Vertragsart (KV ohne Verkäufer), Rundungsregel der Abschreibung, Bezugsvertrag optional je Vertragsart (späterer Kauf → erster Kauf), Vertrag löschen, solange nichts gebucht ist (Fehlerfassung) | 0.37.0 |
| `procurement` | `procurement` (`/m/procurement`, `console procurement:…`) | Beschaffung: Angebote und Eingangsrechnungen von Handwerkern und Dienstleistern, Buchung über die Vorerfassung, Kosten eines Mieters, Pflicht zum Leistungszeitraum, Steuerkennzeichen je Position, Soll/Haben-Ausgleich, Rechnung aus Vertragsvorlage, Rechnung zum Vertrag (Vorbelegung aus den Konditionen), Verwerfen nicht gebuchter Rechnungen, „Vertragskonditionen übernehmen …“, Angebot und Rechnung mit Positionen (Menge, Einheit, Einzelpreis, Gesamtpreis) netto, nachgelagerte Positionen aus dem Vertragsmodul bzw. eigener Tabelle, Positionen aus dem Angebot, Kalkulationsschemas mit Prüfung laut Rechnung, gelesene Zeilen als Betrag laut Rechnung, Rechnung aus mehreren Vorlagen (netto, Umsatzsteuer als eigene Zeile), Rundungsdifferenz der Steuer in der Steuerzeile (Toleranz an der Rechnungsart), nicht abziehbare Vorsteuer auf eigenes Konto, aufzuteilende Vorsteuer nach Quote zum Leistungsdatum, Vorsteuerberichtigung als Vorerfassung, Vorlage je Position | 0.11.3 |
| `opcost` | `opcost` (`/m/opcost`, `console opcost:…`) | Betriebskosten: Kostenarten (umlagefähig nach BetrKV, nicht umlagefähig, Rücklagenzuführung), Verteilerschlüssel und Nebenkostenabrechnung (Regelwerke, Läufe, Rechenweg, Freigabe, Buchung); Abrechnungskonten mit Ausweisgruppe, Eingaben am Lauf (Heizkosten, CO2, Vorauszahlungen), Ebenen (Summe, Vorauszahlungen, Saldo) mit Buchung je Saldo-Regel, Aufstellung ohne Buchung (haushaltsnahe Dienstleistungen), Vorlagen je Regelwerk, Gesamtschlüssel des Gebäudes (WEG); Abrechnungsschreiben als Serienbrief (Datenformat opcost.settlement/1, Absender = Eigentümer bzw. Verwalter des Mietobjekts, Unterschrift aus dem DMS, Stellung aus den Texten des Absenders, Bankverbindung des Mieters) , Spalten der Regeln kurz überschrieben , Kostenaufstellung je Einheit ohne Verteilung (z. B. Unterlagen für den Messdienst) mit Belegliste und Rechnungskopien aus dem DMS | 0.6.1 |
| `bank` | `bank` (`/m/bank`, `console bank:…`) | Bank: Kontoumsätze per CSV einlesen (Spaltenzuordnung, Upload über die Webseite), maschinell und von Hand zuordnen (gelernte Regeln nach Bestätigung), in der Reihenfolge der Zahlungen buchen, Filter nach IBAN der Gegenseite | 0.1.9 |
| `dms` | `dms` (`/m/dms`, `console dms:…`) | Dokumentenmanagement: Ablageplan (Mindmap einlesen), Ablagen je Buchungskreis, Ablagefristen, Archive (Verzeichnisbaum, nur lesbar), Eingangsverzeichnisse mit Vorgaben, Workflows (Texterkennung, Muster, KI, Benutzerfragen), Viewer, Volltextsuche, Prüfung von Buchungen und Verträgen, Eingangsrechnung aus Rahmenvertrag im Workflow, KI-Rechnung mit Vorbelegung aus dem Vertrag, Frage „Vertragspositionen übernehmen?“, KI-Anfragen mit Nettopositionen, KI setzt das Kalkulationsschema, Geschäftspartner anlegen mit Vorschau, Hinweisen aus Dokument und Kontoauszug und Datei an die KI auf Nachfrage, Partnerauswahl je Kategorie eingeschränkt, Workflows Form 2 (acht Schrittarten, Bedingungen, Zuordnungen, Befehlskatalog, Erkennungsregeln, Ablauf je Dokument, Probelauf), Ablagefindung über Kostenart und „umlagefähig“, Vertragsposition im Workflow, KI-Anfragen mit Vorbelegung der abhängigen Auswahlfelder, vorhandene Rechnung zum Vertrag im Workflow wählen (Schritt 142), verworfenes Dokument wieder aufnehmen , Eingang zeigt nur offene Dokumente, Eingangsweg am Dokument | 0.13.4 |
| `template` | `template` (`/m/template`, `console template:…`) | Vorlagen: Go-Templates in Bundles (Ketten mit Blöcken), Ausgabe HTML, XML, CSV und PDF (LaTeX über eingebettetes Tectonic), Aufträge mit Nummer, Status-Event und Ergebnis, Aufbewahrung je Buchungskreis, Beispiel Tilgungsplan (PDF); Briefe und Serienbriefe (letter-class → letter: Kopf-/Fußzeilen erste und Folgeseiten, Absenderzeile, Anschrift, Ort/Datum, Unterschrift aus den Daten files, Stellung über den Text SIGNATURE am Absender), Abrechnungsschreiben (settlement-letter, Datenformat opcost.settlement/1; Guthaben mit Bankverbindung des Mieters) , Kostenaufstellung (COST_SHEET), allgemeine Berichtsvorlage BERICHT (PDF quer, Abschnitte, Zwischensummen) für „Skripte und Berichte“, ANLAGE_V (Einkünfte aus Vermietung je Objekt, Hochformat), MIETEINNAHMEN (Ist je Mieter und Monat, quer) | 0.9.0 |

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
(z. B. `ledger-0.14.0-linux-amd64`), Windows- und Linux-Builds liegen also nebeneinander.

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
- Eingegebene IDs beginnen mit der ID der übergeordneten Ebene – außer mit der Einstellung
  `id_prefix_required: false` (seit 0.3.2, `settings.modules.realestate`): dann sind sie frei,
  z. B. übernommene Schlüssel wie `RUB0101`. Vorschläge bleiben hierarchisch.

### Regeln

- Nutzungsarten gelten für bestimmte Objektarten (z. B. `POOL` nur für Pools).
- **Vertragsobjekt:** enthält Mieteinheiten, Stellplätze und Flächen (keine Pools oder
  Vertragsobjekte); ein Objekt gehört zu einem Zeitpunkt zu **höchstens einem**
  Vertragsobjekt. Eine Zuordnung endet mit „Beenden …“.
- **Pool:** Die aus ihm geschnittenen Flächen ergeben zum Stichtag zusammen höchstens seine
  Gesamtfläche (in seiner Flächenart, z. B. Nutzfläche) – geprüft beim Erfassen der
  Bemessungen, beim Zuordnen von Flächen und beim Ändern der Gesamtfläche.
- **Gebäudefläche** (seit 0.3.3): Die Mieteinheiten und Pools eines Gebäudes haben zum
  Stichtag zusammen höchstens die Fläche des Gebäudes in derselben Bemessungsart (ohne Wert am
  Gebäude keine Prüfung). Für WEG-Anteile gibt es die Bemessungsart `MEA` (Tausendstel `TSD`
  oder Zehntausendstel `ZTSD`) und den Verteilerschlüssel `MEA` der Betriebskosten.
- **Stufen der Prüfungen** (seit 0.3.5): Jede fachliche Prüfung der Bemessungen ist
  einstellbar – Fehler (nicht speichern), Warnung (speichern, gelbe Meldung) oder keine
  Prüfung. An der Art der Wirtschaftseinheit: Gebäudefläche (`area_check_level`; Vorschlag
  Eigenbestand `OWN` Fehler, Eigentumswohnungen in WEG `ETW`, Verwaltungsarten keine – dort
  sind oft nur einzelne Wohnungen erfasst) und Pool (`pool_check_level`, Vorschlag Fehler);
  an der Bemessungsart: negativer Wert (`negative_check`, Vorschlag Fehler). Fest bleiben nur
  Prüfungen der Datenintegrität (Objekt, Bemessungsart und Maßeinheit müssen existieren).

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
| `contract__condition` | `ContractCondition` | Konditionen: **Menge × Einzelpreis = Gesamtpreis je Periode** (seit 0.22.0, Mengeneinheit, Rundungsregel), je Einheit einer Bemessung (Einzelpreis × Bemessung) oder **nachgelagert** (Art aus `ContractDependentType`), Rhythmus, Fälligkeit, Zahlungsweise, Sachkonto, abweichender Zahler, Zeitscheibe |
| `contract__dependent_type` | `ContractDependentType` | Nachgelagerte Konditionsart je Buchungskreis (seit 0.22.0): **Steuer** (Satz aus dem Steuerkennzeichen; leer = das der Konditionsart der Basis) oder **Prozent**; **Zuschlag** (kommt hinzu, z. B. Umsatzsteuer) oder **Anteil** (aus der Basis herausgerechnet, z. B. Materialanteil eines Hausmeistervertrags); Basis (Konditionsarten, leer = alle außer Steuer), Sachkonto, Rundungsregel, Standard für Rechnungen ohne Vertrag |
| `contract__notice_term` | `ContractNoticeTerm` | Kündigungsfrist, Stichtag, Mindestlaufzeit, Verlängerungsoption, Zeitscheibe |
| `contract__contract_type` | `ContractType` | Vertragsart je Buchungskreis: Richtung, Rolle des Vertragspartners, Nummernkreis, Objektpflicht, erlaubte Objekte, exklusive Objektnutzung |
| `contract__condition_type` | `ConditionType` | Konditionsart je Buchungskreis: **Haupt- oder Nebenforderung**, Vorauszahlung, Verrechnungsreihenfolge, Steuerkennzeichen, Rundungsregel |
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
Debitor, WEG-Verwalter, Hausmeister, Bürge, Zahler, Behörde / Amt – Partner der Grundsteuer), Vertragsarten (MV Wohnraummiete, GM
Gewerbemiete, SP Stellplatz, HG Hausgeld, VV WEG-Verwaltervertrag, DL Dienstleistung, VS
Versicherung, VE Versorgung, SO Sonstiges – nur mit aktivierter Rolle) und Konditionsarten (KM
Kaltmiete, NK/HK Vorauszahlungen, ST Stellplatz, HG Hausgeld, EN Entgelt/Prämie, MG
Mahngebühr, ZI Verzugszinsen, UST Umsatzsteuer) und die nachgelagerte Konditionsart **UST
Umsatzsteuer** (Steuer mit dem Kennzeichen der Konditionsart der Basis, Standard für Rechnungen
ohne Vertrag; seit 0.22.0). Eine Kondition mit Berechnung „nachgelagert“ und Art UST am Vertrag
rechnet die Umsatzsteuer auf die anderen Konditionen. Die Kontenfindung pflegt der Buchungskreis selbst.
`procurement:setup-company` legt UST auch in der eigenen Tabelle der Beschaffung an (gilt ohne Vertragsmodul).

Außerdem die **Mieter-Merkmale** im Tag-Plugin (sofern gestartet): Tags Steuer-ID, Geburtsdatum,
Personalausweis-Nummer, -Ausstellungsdatum, -Behörde und Briefanrede, das Tag Set `MIETER` und
seine Zuordnung zu Geschäftspartnern im Buchungskreis, die natürliche Personen sind **und** heute
die Rolle Mieter haben (Abschnitt „Merkmale“). Steuer-ID, Geburtsdatum und Ausweisdaten sind
**geschützt** (Rechte „Werte geschützter Tags sehen/ändern“, tag 0.4.0); Steuer-ID und
Ausweis-Nummer haben ein Prüfmuster. Bestehende Definitionen bleiben unverändert. Den Personalausweis selbst legt man als Dokument
der Art „Personalausweis“ (document 0.1.1) am Partner ab.

**Versorgungsverträge** (seit 0.25.0): Tag Set `VERSORGUNG` am Vertrag der Vertragsart VE. Das
Pflicht-Tag **Sparte** (Strom, Gas, Fernwärme, Wasser / Abwasser) steuert über Regeln „sichtbar wenn“
die weiteren Tags: für alle Zählernummer, Netzbetreiber, Verbrauchsprognose, Kalkulationsschema der
Rechnungen; bei Strom Marktlokations-ID (11 Ziffern) und Messlokations-ID (DE + 31 Zeichen); bei
Fernwärme Anschlussleistung (kW) und Übergabestation. So braucht es keine eigene Vertragsart je Sparte.

### Anbindung

- **Immobilien:** Das Modul abonniert `realestate.partners` (Phase modify): „Wirksame
  Partner“ eines Mietobjekts zeigt auch die Partner der Verträge (aktiv oder gekündigt), z. B.
  den Mieter – direkt oder über ein Vertragsobjekt, zu dem das Objekt gehört.
- **Hauptbuch:** Die Kontierung „Mietvertrag“ (`rent_contract_id`) ist eine Auswahl auf
  `Contract`; Sachkonten der Kontenfindung prüft das Modul über `GLAccountCompany`.
- **Rechte:** `Contract.read` mit dem Feld `contract_type` (z. B. nur Versicherungen),
  Kataloge mit eigenem Recht; SystemEvents bei jeder Änderung und bei Statuswechseln.
- **Merkmale (Tags)** im Vertrag, Tag Sets über die Bedingung auf `contract_type`.
- **Hooks:** `contract.activate` (check, commit) beim Aktivieren; `contract.condition` (check)
  bei neuer Kondition bzw. geändertem Betrag oder Beginn – Daten: Vertrag, Vertragsart,
  Konditionsart, Objekt, Beginn, Betrag, Beginn der vorigen Zeitscheibe. E verhindert das
  Speichern, W kommt als Warnung (z. B. DMS: Hausgeld nur mit Dokument).
- **Kontonummern** der Kontenfindung und der Konditionen speichert das Modul als reine Nummer
  (`6000`), wie das Hauptbuch sie führt; Altbestand mit Kontenplan-Präfix stellt die Migration um.

### Vertragsübersicht (Report)

Menü **Verträge → Auswertungen → Vertragsübersicht**: Buchungskreis, Vertragsarten (Mehrfachauswahl,
keine = alle), optional Wirtschaftseinheit, Gebäude oder Mietobjekt (mit allen Objekten darunter),
Zeitraum und Vorlage (Verwendung `CONTRACT_REPORT`, Standard: `VERTRAEGE` PDF quer, `VERTRAEGE_XLS`
Excel-XML). Gezeigt werden laufende Verträge (aktiv oder gekündigt). Der Report baut die
Objektstruktur aus den Immobilien auf (Wirtschaftseinheit → Gebäude → Mietobjekt; Bestandteile
eines Vertragsobjekts und geschnittene Flächen darunter) und zeigt jeden Vertrag einmal unter der
obersten Einheit seines Hauptobjekts, darin nach Vertragsart. Konditionen stehen mit ihrer
Gültigkeit im Zeitraum (jede Zeitscheibe eine Zeile); die Beträge im Zeitraum sind tagesgenau je
Monat abgegrenzt (einmalige im Monat der Fälligkeit). Summen nur je oberster Einheit und gesamt,
dazu die Monatsübersicht (Bildschirm, PDF, Excel-Blatt „Monate“). Kopfzeile: links der
Geschäftspartner des Buchungskreises, in der Mitte oberste Einheit und Vertragsart der Seite;
Fußzeile: Anschrift.

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
- **Aktivieren ohne Konditionen** (seit 0.10.5): Ohne Konditionen oder Darlehenskonditionen lässt
  sich ein Vertrag nur aktivieren, wenn die Vertragsart es erlaubt (`without_conditions`, Vorschlag:
  WH „Hausgeld an WEG“ – gebucht wird dann erst mit der Abrechnung der WEG).
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
  rechnet contract-billing; Aktion **„Tilgungsplan …“** am Vertrag: ohne Vorlage als Tabelle, mit Vorlage der Verwendung `LOAN_SCHEDULE` als Datei; „Plan bis“ (Vorschlag: Vertragsende, unbefristet 30 Jahre) (Beispiel TILGUNGSPLAN, PDF; „Beispiele anlegen …“ bzw. `template:setup-examples`).
- **Konditionen kopieren** (seit 0.12.0, Aktion am Vertrag): die zum Stichtag gültigen Konditionen
  ab „Gültig ab“ **fortschreiben** (selber Vertrag – z. B. neues Wirtschaftsjahr; die alten enden am
  Vortag) oder auf einen **anderen Vertrag kopieren** (z. B. nächste Wohnung derselben WEG). Mit
  „Anteil neu rechnen nach“ (WFL, MEA …) werden feste Beträge im Verhältnis der Bemessungen der
  Hauptobjekte umgerechnet; Konditionen „je Einheit“ rechnen ohnehin mit dem Zielobjekt.
- **Wirtschaftsplan** (seit 0.13.0, Abschnitt „Wirtschaftspläne“ am Vertrag, z. B. am Hausgeld-Vertrag
  WH): Kopf `ContractPlan` (Wirtschaftsjahr, beschlossen am, Bezeichnung) mit **jeder Zeile des
  Plans** (`ContractPlanItem`): Bezeichnung laut Plan, Kostenart (Modul Betriebskosten – deren Art
  entscheidet umlagefähig, nicht umlagefähig, Rücklage), Gesamtkosten, Verteilerschlüssel mit
  Gesamt- und Anteilswert, Ihr Anteil. Weicht der Anteil um mehr als die Toleranz der Vertragsart
  von Gesamtkosten × Anteil / Gesamt ab, kommt eine Warnung. Summen am Kopf: gesamt, umlagefähig,
  nicht umlagefähig, Rücklage, monatlich. **„Positionen aus dem Vorjahr übernehmen“** wie bei der
  Abrechnung (Beträge leer). Die Konditionen (z. B. HV, RZ) bleiben die Zahlungsbedingungen. Die
  **Abrechnung** desselben Zeitraums zeigt **Plan und Ist**: je Position den Planwert der Kostenart
  und die Abweichung, am Kopf Plan, Ist (Summe der Einzelbeträge) und Abweichung.
- **Rhythmus und Periodenbeginn** (seit 0.15.4 / 0.16.0): Konditionen monatlich, **zweimonatlich**,
  vierteljährlich, halbjährlich, jährlich oder einmalig. „Periodenbeginn im Monat“ verschiebt die
  Perioden vom Kalender weg, z. B. 10 = Versicherungsjahr Oktober bis September (fällig am 01.10.
  statt am 01.01.); leer = Kalender.
  „Fällig im … Monat der Periode“ (seit 0.17.0, vorschüssig) legt die Fälligkeit in einen späteren
  Monat, z. B. 3 = Jahresmiete Januar bis Dezember, fällig am 15.03.
- **Abschnitte je Vertragsart** (seit 0.15.0): `setup-company` legt je Standard-Vertragsart eine
  Darstellungsregel an („Vertrag: DA Darlehen (aufgenommen)“ …), die die unpassenden Abschnitte
  ausblendet – Darlehen/Sondertilgungen nur bei DA/DV, Abschreibung bei KV, Bankkonto bei BK/KK,
  Personen bei MV, Pläne/Leistungsverzeichnisse bei WH/HG/DL/VE, Abrechnungen bei WH/HG/VE/GS/DL/VS. Danach gehören
  die Regeln dem Administrator (Administration → Darstellung); eigene Vertragsarten zeigen alles.
- **„Buchen …“ ohne Ergebnis nennt den Grund** (seit 0.15.0): Vertrag nicht aktiv, Sollstellung ab
  nach „Buchen bis“, keine Konditionen, Konditionen erst später gültig (mit Art und Datum), schon
  alles gebucht oder nichts fällig.
- **Wirtschaftspläne exportieren und importieren** (seit 0.14.0) als lesbare YAML-Datei, die
  Releasewechsel übersteht (Fachobjekte, nicht Tabellen; `format: coremesh.contract-plans`,
  `version: 1`):
  ```bash
  console contract:export-plans --company=2000 [--contract=WH-2026-0001] [--from=2026-01-01] --out=plaene.yaml
  console contract:import-plans --company=2000 --file=plaene.yaml [--dry-run]
  ```
  Je Plan Vertrag (`contract_id`, sonst der Vertrag mit dem Hauptobjekt `object_id`), Zeitraum,
  Bezeichnung und die Positionen. Vorhandene Pläne mit Positionen bleiben unverändert; geprüft
  wird wie bei der Erfassung, Warnungen werden gemeldet.
- **Abschreibung** (seit 0.11.0): Vertragsart `KV` „Kaufvertrag Immobilie“ (Objekt Haus, Wohnung
  oder Wirtschaftseinheit, ohne Partnerkonto; seit 0.34.0 ohne Vertragspartner – Schalter
  „Vertragspartner Pflicht“ an der Vertragsart, Vorschlag bei KV aus; ohne Partner bildet die
  externe Nummer das Kürzel aus der Bezeichnung) mit **Abschreibungskondition**
  (`ContractDepreciation`, Zeitscheiben): Bemessungsgrundlage Gebäude (kumuliert – höhere
  Zeitscheibe = nachträgliche Herstellungskosten), Anteil Grund und Boden, linear oder degressiv,
  Satz, Beginn, Restbuchwert bei Übernahme, Anlage- und Abschreibungskonto, Konditionsart `AF`;
  seit 0.33.0 **Jahresbetrag fest** (linear, z. B. laut Steuerbescheid) und **Raten im Jahr**
  (1, 2, 3, 4, 6, 12; Rundungsrest in der letzten Rate des Jahres), gebucht ab „Sollstellung ab“;
  seit 0.35.0 **Rundung des Jahresbetrags** (Rundungsregel des Hauptbuchs, z. B. auf volle Euro
  aufrunden; leer = Regel der Konditionsart AF, sonst Cent). Je Kauf ein KV (mehrere Wohnungen als
  Objekte, eine Abschreibung); ein späterer Kauf im selben Haus verweist seit 0.36.0 als
  Bezugsvertrag auf den ersten (Vertragsart: „Bezugsvertrag Pflicht“ aus, erlaubt KV) und steht
  dort unter „Zugehörige Verträge“.
  contract-billing rechnet je Jahr bis 31.12. bzw. Vertragsende monatsgenau und bucht
  Abschreibungskonto an Anlagekonto (Belegart SA, ohne Partnerzeile); Aktion **„Abschreibungsplan“**.
- **Bankkonto und Kreditkarte** (Vertragsarten BK, KK – die Karte mit dem Bankkonto als
  Bezugsvertrag; `ContractBankAccount`, Zeitscheiben): Art, Inhaber, IBAN (Prüfziffer) und BIC,
  von der Karte **nur die letzten 4 Ziffern** (die volle Nummer wird abgewiesen), gültig bis,
  Abrechnungstag, Belastung über einen Bankkonto-Vertrag (Vorschlag: Bezugsvertrag), Rahmen,
  Soll-/Habenzins, Sachkonto im Hauptbuch (je Zeitraum höchstens ein Vertrag je Sachkonto).
  Entgelte sind Konditionen (KF Kontoführungsentgelt, KJ Kartengebühr).
- **Sollstellung ab** (`billing_start`, seit 0.10.0): Stichtag, ab dem die Sollstellung bucht – z. B.
  bei Verträgen, die aus einem Altsystem übernommen werden (davor ist dort gebucht). Leer =
  Vertragsbeginn; nicht vor dem Beginn.
- **Personen** (`ContractPersons`, optional, Zeitscheiben innerhalb der Laufzeit): Anzahl der
  Personen eines Mietvertrags – nur nötig, wo die Nebenkostenabrechnung nach Personen verteilt.
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

### Nebenkostenabrechnung

| Tabelle | Object | Inhalt |
|---|---|---|
| `opcost__definition` | `SettlementDefinition` | Regelwerk je Abrechnungseinheit (Gebäude oder Wirtschaftseinheit) – beliebig viele, z. B. Nebenkosten und haushaltsnahe Dienstleistungen: Vertragsarten der Mieter, Konditionsarten der Vorauszahlungen, **Rechengenauigkeit** (Faktor auf Cent, Standard 100000), **Rundungsregel**, Ledger, Konten und Belegarten der Buchung |
| `opcost__rule` | `SettlementRule` | Regel: Schritt, Art **Sammeln** (Quelle → Topf), **Umbuchen** (Topf → Topf, Anteil %), **Verteilen** (Topf → Mietobjekte → Mieter nach Schlüssel, tagesgenau mit Leerstand), Kostenart für den Ausweis |
| `opcost__run` | `SettlementRun` | Lauf je Zeitraum: Entwurf → **Rechnen** (beliebig oft) → **Freigeben** → **Buchen …**; Ergebnis und Prüfungen; **Abrechnungsschreiben …** (Vorlage der Verwendung OPCOST_SETTLEMENT) |
| `opcost__journal` | `SettlementJournal` | Rechenweg: doppelte Buchungen auf internen Abrechnungskonten mit Formel und Quelle, Betrag in Cent × Faktor |
| `opcost__tenant` | `SettlementTenant` | Ergebnis je Mieter: Nutzung, Kosten je Kostenart, Vorauszahlungen, Nachzahlung/Guthaben, Rechenweg je Konto, Buchung |

- **Quellen** der Sammelregeln: Sachkonto (Einzelposten im Zeitraum), Eingangsrechnungen nach
  Kostenart (Leistungszeitraum anteilig), gebuchte Vertragsabrechnungen nach Kostenart (z. B.
  WEG, anteilig nach Abrechnungszeitraum). Kontierung auf ein Mietobjekt bzw. einen Mietvertrag
  geht direkt dorthin (Grundsteuer einer Wohnung, zusätzliche Anfahrt eines Mieters) – Rechnungen
  werden dafür in Positionen geteilt; Kosten fremder Objekte gehören nicht zur Einheit.
- **Abrechnungskonten:** `Q:` Quelle, `S:<Topf>`, `O:<Objekt>:<Topf>`, `M:<Vertrag>:<Topf>` (Mieter),
  `E:<Objekt>:<Topf>` (Eigentümer: Leerstand, Rundung), `R:` Rundung. Was am Ende auf `S:` oder
  `O:` liegt, meldet die Prüfung als liegengeblieben; Summe der Quellen = Mieter + Eigentümer.
- **Schlüssel:** Bemessung (zeitgewichtet), Anzahl Einheiten, **Personen** (Personentage aus den
  Personen-Zeitscheiben am Vertrag, direkt auf die Mieter), direkt (nur Kontierung). Verbrauch
  (Zähler) folgt.
- **Rundung:** jeder Anteil abgerundet, Rest auf `R:`; am Ende Mieter und Eigentümer auf Cent.
  Regel „Mieter oder Eigentümer“: Rundung in Cent ≥ Anzahl Mieter → je Mieter gleich viel, Rest an
  den Eigentümer; sonst alles an den Eigentümer.
- **Buchen …** (nur freigegeben, eine Transaktion): je Mieter ein Beleg – Soll Konto der
  Vorauszahlungen, Haben Erlöse abgerechnete Betriebskosten, Differenz an das Mieterkonto
  (Abstimmkonto in der Rolle der Vertragsart); Belegart nach Saldo, Belegdatum Ende des Zeitraums.
- **Zuordnung zum Zeitraum** je Sammelregel: Leistungs- bzw. Abrechnungszeitraum (anteilig nach
  Tagen; Vorschlag für Rechnungen und Vertragsabrechnungen), Buchungsdatum (Vorschlag für
  Sachkonten – Einzelposten haben keinen Leistungszeitraum) oder Belegdatum. So gehört die im
  Jahr X+1 gebuchte Versorgerabrechnung für X steuerlich ins Jahr X+1 (Buchungsdatum im
  Hauptbuch) und in die Nebenkostenabrechnung X (Abrechnungszeitraum). Eine Rechnungsposition
  ohne Leistungszeitraum fällt auf das Buchungsdatum zurück und erscheint als Abweichung.
- **Vollständigkeit** (Quelle Vertragsabrechnung) je Regel und Vertrag: Lücken im Zeitraum
  (innerhalb der Vertragslaufzeit), „im Vorjahr abgerechnet – fehlt“, „erfasst, aber nicht
  gebucht“ sind **Fehler**. Mit Fehlern ist der Lauf nicht freigebbar, außer das Regelwerk
  erlaubt „Freigabe trotz Fehlern“.
- Gerechnet wird im Haskell-Plugin `contract-billing` (`ContractBilling.computeOpCost`).

## Beschaffung (`procurement`)

Angebote und Eingangsrechnungen von Handwerkern und Dienstleistern, in der kleinsten Einheit der Währung.

**Positionen (seit 0.8.0):** Angebot und Rechnung haben Positionen mit Menge, Mengeneinheit,
Einzelpreis und Gesamtpreis (Menge × Einzelpreis nach der Rundungsregel), **netto**. Die
nachgelagerten Positionen (Feld „Nachgelagerte Konditionen“ am Kopf) rechnet das Modul nach jeder
Änderung: aus dem Vertragsmodul (`ContractDependentType`; Rechnung zum Vertrag: dessen
nachgelagerte Konditionen), ohne Vertragsmodul aus der eigenen Tabelle (`ProcurementDependentType`);
leer beim Anlegen = Standard, „-“ = keine. Umsatzsteuer je Steuerkennzeichen der Positionen:
abziehbar auf das Vorsteuerkonto, nicht abziehbar in den Aufwand der Basispositionen verteilt;
ein Anteil (z. B. Materialanteil) mit eigenem Konto wird aus der Basis herausgerechnet. Der
**Kostenbetrag** jeder Position (netto − Anteil + verteilte Zuschläge und nicht abziehbare Steuer)
geht in Buchung und Nebenkostenabrechnung. Ältere Rechnungen (vor 0.8.0) bleiben brutto.
„Positionen aus dem Angebot übernehmen …“ ersetzt die Positionen durch die des angenommenen Angebots.

**Kalkulationsschema (seit 0.9.0):** Eine Kette abhängiger Positionen wird einmal definiert
(Beschaffung → Einstellungen → Kalkulationsschemas) und an Hauptpositionen gehängt (Feld
Kalkulationsschema). Beispiel STROM am Arbeitspreis (3.500 kWh × 0,2850): Schritte Netzentgelt,
Konzessionsabgabe, Stromsteuer je kWh (Rechenregel „Menge des Bezugs × Satz je Einheit“) und ein
Zuschlag in Prozent vom Netzentgelt (Bezug auf Schritt 10). Jeder Schritt hat Bezug (0 =
Hauptposition oder ein früherer Schritt), Satz mit Gültigkeit (maßgeblich: Leistungsbeginn der
Hauptposition, sonst Belegdatum), Einheit, Steuerkennzeichen, Kostenart und Sachkonto (leer = wie
die Hauptposition), Rundungsregel. Die abhängigen Zeilen erscheinen unter der Hauptposition („zu
Position“) und folgen jeder Änderung der Menge; der Satz lässt sich an der Zeile überschreiben
(abweichender Satz des Versorgers), einzeln entfernen lässt sie sich nicht. **Prüfung:** „Betrag
laut Rechnung“ je Zeile zeigt die Abweichung zum gerechneten Betrag, der Kopf die Summe der
Abweichungen. Die Umsatzsteuer rechnet danach wie bisher je Steuerkennzeichen auf alle Positionen. Liest die
KI eine Rechnung mit allen Zeilen, werden Zeilen ohne Schema einer abhängigen Zeile mit gleichem Text
zugeordnet (seit 0.9.1): ihr Betrag wird „Betrag laut Rechnung“, die gelesene Zeile entfällt. An der
**Vertragskondition** (contract 0.24.0) lässt sich das Schema ebenfalls wählen; Rechnungen zum Vertrag
übernehmen es (die Sollstellung bucht nur die Kondition selbst, z. B. die Abschläge).

| Tabelle | Object | Inhalt |
|---|---|---|
| `procurement__invoice_type` | `InvoiceType` | Rechnungsart je Buchungskreis: Belegart im Hauptbuch (Rechnung, Gutschrift), **Rolle des Lieferanten** (Finanzrolle mit Abstimmkonto), Nummernkreis, automatisch buchen |
| `procurement__object_posting` | `ObjectPosting` | Kontierung der Objekte: welches Feld im Hauptbuch eine Objektart bekommt (Vorschlag: Mietobjekt → `rent_object_id`, Gebäude → Dimension 1, Wirtschaftseinheit → Dimension 2) |
| `procurement__quote` | `PurchaseQuote` | Angebot `AN-<Jahr>-<n>`: Lieferant, Leistung, Betrag, gültig bis, Objekt, Kostenart; **Annehmen** / **Ablehnen** |
| `procurement__invoice` | `SupplierInvoice` | Eingangsrechnung `<Rechnungsart>-<Jahr>-<n>`: Lieferant, Rechnungsnummer des Lieferanten (je Lieferant eindeutig), Rechnungs-/Buchungsdatum, Fälligkeit, angenommenes Angebot, Objekt; Status erfasst → vorerfasst → gebucht → storniert, oder (nie gebucht) verworfen; **Vertrag** (Rechnung zum Vertrag, Rahmenvertrag) |
| `procurement__invoice_item` | `SupplierInvoiceItem` | Position: Kostenart (Modul Betriebskosten), Sachkonto (Vorschlag der Kostenart), Betrag (negativ = Gutschrift), Objekt (leer = Rechnung), Kostenstelle, **Mietvertrag** (Kosten eines Mieters, z. B. zusätzliche Anfahrt des Messdienstes), **umlagefähig**, **Leistungszeitraum** (Pflicht bei umlagefähiger Kostenart, wenn die Rechnungsart es verlangt) |

- **„Buchen …“** an der Rechnung: Vorerfassung im Hauptbuch – je Position Aufwand im Soll,
  Gegenposition Haben auf das **Abstimmkonto des Lieferanten** (Buchungskreisdaten in der Rolle
  der Rechnungsart, Buchungssperre wird beachtet) mit dem Lieferanten als Partner; Kontierungen
  nur, wo der Feldstatus des Kontos sie zulässt; Belegart der Rechnungsart, bei negativem
  Saldo die Gutschrift-Belegart. Dann prüfen und bei „automatisch buchen“ buchen – alles in
  einer Transaktion: Scheitert die Prüfung, bleibt die Rechnung erfasst.
- Wird die Vorerfassung im Hauptbuch gebucht oder verworfen, folgt die Rechnung
  (SystemEvents `JournalDraft.post`/`deactivate`).
- **„Verwerfen …“** (seit 0.6.0): eine nicht gebuchte Rechnung (erfasst oder vorerfasst, dann mit
  ihrer Vorerfassung) ist verworfen – ohne Beleg. **„Stornieren …“** gilt nur gebuchten Rechnungen:
  Stornobeleg im Hauptbuch zum angegebenen Datum. Ältere „stornierte“ Rechnungen ohne Stornobeleg
  stellt das Update auf verworfen um.
- **Rechnung zum Vertrag:** Das Feld Vertrag ist änderbar und wird geprüft; `contractDraft`
  (`company_code`, `contract_id`, `date`) liefert den Entwurf: Lieferant, Währung, Objekt, Steuer der
  Vertragsart und je am Datum gültiger Kondition eine Position (Text, Sachkonto, Betrag, Objekt).
- **„Vertragskonditionen übernehmen …“** (seit 0.7.0, nur erfasste Rechnungen): ersetzt die Positionen
  durch die am Datum (Vorschlag: Rechnungsdatum) gültigen Konditionen des Vertrags (Vorschlag: Vertrag
  der Rechnung); Vertrag, Objekt und Steuer kommen an die Rechnung, wenn dort leer.
- Positionen sind nur änderbar, solange die Rechnung erfasst ist; „Inaktivieren“ entfernt eine.
- **Einrichten:** `console procurement:setup-company --company 1000` bzw. „Buchungskreis
  einrichten …“ an den Rechnungsarten. Kostenarten seit 0.2.0 im Modul Betriebskosten.
- Regelmäßige Kosten mit Vorauszahlung (Grundsteuer, Wasser, Hausgeld) gehören nicht hierher,
  sondern als Vertrag mit **Vertragsabrechnung** ins Vertragsmodul.
- Die Positionen mit umlagefähig, Objekt und Leistungszeitraum sind die Grundlage des
  Nebenkostenrechners.

## Bank (`bank`)

Kontoumsätze einlesen, den Vorgängen zuordnen und im Hauptbuch buchen. Beträge in der kleinsten
Einheit der Währung, Eingang positiv, Ausgang negativ.

| Tabelle | Object | Inhalt |
|---|---|---|
| `bank__account` | `BankAccount` | Bankkonto: IBAN (Prüfziffer, je Buchungskreis eindeutig), BIC, Währung, **Sachkonto Bank**, Belegarten (Debitorenzahlung DZ – auch Rückzahlungen an Mieter, Kreditorenzahlung KZ, Sachkonto SA), Vorschlag Importformat, optional der Vertrag (BK) |
| `bank__format`, `bank__format_column` | `BankImportFormat`, `BankImportColumn` | Importformat (CSV): Trennzeichen, Kopfzeile, Datums- und Dezimalformat, Reihenfolge der Datei, Soll/Haben-Werte; je Spalte das Feld des Umsatzes, Aufbereitung (Trimmen, Großschreibung, Vorzeichen umkehren, Auszug per regulärem Ausdruck, IBAN im Text finden) und Ersatzspalte |
| `bank__import` | `BankImport` | Einlesung: Datei, gelesen, neu, schon vorhanden, Zeitraum |
| `bank__transaction` | `BankTransaction` | Umsatz: Daten der Datei (Gegenseite, IBAN, Verwendungszweck, Referenzen), Status offen → Vorschlag → zugeordnet → gebucht (oder ignoriert), Ziel (Vertrag, Partner + Rolle, Eingangsrechnung, Sachkonto), Begründung, **Beleg und Buchungssätze** |
| `bank__rule` | `BankRule` | Zuordnungsregel: wenn Bankkonto, Richtung, Name/IBAN der Gegenseite, Verwendungszweck, Betrag – dann Ziel; gelernt als Vorschlag, wirkt nach „Bestätigen“ |

- **Einlesen:** „Kontoauszug einlesen …“ am Bankkonto (Datei-Upload) oder
  `console bank:import --company=1000 --account=GIRO --file=./umsaetze.csv`. Die Datei muss zum
  Konto passen (Spalte „IBAN des eigenen Kontos“). Doppelte Umsätze erkennt ein Fingerabdruck
  (Datum, Betrag, Gegenseite, Texte; gleiche Umsätze am selben Tag zählen einzeln). Danach wird
  gleich zugeordnet.
- **Zuordnen** (maschinell, Reihenfolge): bestätigte Regel (die genaueste) → Vertragsnummer
  (intern/extern) oder BP-Nummer im Verwendungszweck → Ausgang: Rechnungsnummer des Lieferanten
  im Text (bzw. gleicher Betrag + passender Name: Vorschlag) → IBAN der Gegenseite → Name der
  Gegenseite (nur Vorschlag). Bei einem Partner wird der Vertrag gewählt, wenn genau einer passt
  (Partner oder Mitpartner, Richtung, Laufzeit), sonst der Partner in seiner einzigen Finanzrolle.
- **Von Hand:** „Zuordnen …“ am Umsatz – daraus entsteht ein Regelvorschlag (Gegenseite, IBAN,
  Richtung → Ziel), der erst nach „Bestätigen“ wirkt. Vorschläge: „Vorschlag übernehmen“.
- **Buchen …** in der Reihenfolge der Zahlungen (Buchungstag, dann Reihenfolge der Datei): je
  Umsatz ein Beleg – Eingang Soll Bank an Partnerkonto (Abstimmkonto der Rolle) bzw. Sachkonto,
  Ausgang umgekehrt; Kontierung Vertrag und Kunde bzw. Lieferant nach Feldstatus. Der erste nicht
  zugeordnete Umsatz hält die Buchung an (am Bankkonto abschaltbar). Am Umsatz stehen Beleg und
  Buchungssätze („Gebucht als“).
- **Einrichten:** `console bank:setup-company --company 1000` legt das Importformat `COBA`
  (Commerzbank, CSV-Export der Umsätze) an.

## Dokumentenverwaltung (`dms`)

Dokumente ablegen, wiederfinden und prüfen. Die Dateien liegen in einem **Archiv** (Art `FILE`:
Verzeichnisbaum auf dem Server), die Datenbank kennt Kopf, Bezüge, Frist und Verlauf.

| Tabelle | Object | Inhalt |
|---|---|---|
| `dms__plan`, `dms__account` | `DmsFilePlan`, `DmsFileAccount` | **Ablageplan** wie ein Kontenplan, für alle Buchungskreise: Ablage mit Nummer, Name, übergeordneter Ablage (Vorschlag aus der Nummer: 3.0.1 → 3.0), Ablagefrist. Dokumente nur in der **untersten Ebene**; eine Ablage mit Dokumenten bekommt keine Unterablagen |
| `dms__company`, `dms__account_company` | `DmsCompany`, `DmsFileAccountCompany` | Je Buchungskreis: Ablageplan, Archiv, Kürzel und **Muster des Dateinamens**, Prüfungen ein/aus; aktivierte Ablagen mit abweichender Frist, abweichendem Archiv oder gesperrt |
| `dms__retention` | `DmsRetention` | **Ablagefrist**: Jahre/Monate ab Dokumentdatum oder ab Ende des Kalenderjahres, oder dauerhaft (Vorschläge: AO10, AO8, AO6, Y3, FOREVER) |
| `dms__doc_type` | `DmsDocType` | Dokumentart mit Vorschlag der Frist und der Dokumentart der Verweise (Plugin `document`) |
| `dms__archive` | `DmsArchive` | Archiv: Art und Wurzelverzeichnis. Schnittstelle `Archive` (Store/Load/Remove/Check) – Dokument als Bytes, Ablage mit Pfad, Bezüge, Dokumentdatum, Frist, Dokumentart, Partner; weitere Arten melden sich in `openArchive` an |
| `dms__inbox`, `dms__inbox_param` | `DmsInbox`, `DmsInboxParam` | **Eingangsverzeichnis** je Buchungskreis (Verzeichnis auf dem Server, Workflow) mit **Vorgaben aus Object und Schlüssel**: Ablage, Dokumentart, Ablagefrist, Geschäftspartner oder Bezug (z. B. `Building ROBad1` für das Verzeichnis Görlitz) |
| `dms__workflow`, `dms__step`, `dms__step_option`, `dms__step_condition`, `dms__step_mapping`, `dms__step_text` | `DmsWorkflow`, `DmsWorkflowStep`, `DmsStepOption`, `DmsStepCondition`, `DmsStepMapping`, `DmsStepText` | **Workflow** für alle Buchungskreise, kopierbar (Form 2 seit 0.12.0): Schritte mit Einstellungen je Art, Bedingungen, Zuordnungen, Übersetzungen, Optionen (Ergebnis → nächster Schritt, Knopf) |
| `dms__command` | `DmsCommand` | **Befehle** der Texterkennung und Aufbereitung (Programme nur aus `allowed_programs`) |
| `dms__document_step` | `DmsDocumentStep` | **Ablauf** je Dokument: alle Schritte mit Stand, Ergebnis, Wert und Hinweis; „Hierher zurück“ |
| `dms__document`, `dms__document_ref`, `dms__document_text` | `DmsDocument`, `DmsDocumentRef`, `DmsDocumentText` | **Dokument** im Eingang bzw. abgelegt: Kopf (Titel, Dokumentart, Ablage, Partner, Datum), Dokumentname, Ort im Archiv, Frist, Prüfsumme, Workflow mit Verlauf; Bezüge (Vertrag, Gebäude, Mietobjekt, Eingangsrechnung, Vorerfassung …); erkannter Text |

- **Ablageplan einlesen …** (Webseite) bzw. `console dms:import-plan --plan=IMMO --file=Plan.xml`:
  Mindmap im Freemind-Format, Knoten „1.0.0 Grundbuchauszüge“; der oberste Knoten ohne Nummer
  benennt den Plan. Erneutes Einlesen ergänzt und aktualisiert Namen.
- **Einrichten:** `console dms:setup-company --company=2000 --plan=IMMO --archive=MAIN --root=/srv/archiv --short_name=VV`
  legt Archiv und Einstellungen an, aktiviert alle Ablagen („Ablagen übernehmen“) und den
  Workflow `STANDARD` (Text erkennen → Datum suchen → Dokumentart → Ablage → Objekt → Partner → Datum →
  Titel → bestätigen → ablegen; Vorgaben überspringen die Fragen).
- **Eingang:** „Hochladen …“ am Eingangsverzeichnis (PDF, Bild) oder „Einlesen“ (neue Dateien im
  Verzeichnis, auch `console dms:scan`). Gleicher Inhalt (SHA-256) wird nicht doppelt übernommen.
- **Dokumentkategorie und Dokumentart:** Die Kategorie (`DmsDocCategory`: Vertrag, Rechnung /
  Gutschrift, Gebäude / Mietobjekt, Sonstiges) sagt, zu welchem kaufmännischen Objekt ein Dokument
  gehört; die Dokumentart (`DmsDocType`, gehört zu einer Kategorie) ist die Art, mit der es am Objekt
  gespeichert wird (am Vertrag: Vertrag, Nachtrag, Rechnungsdokument, Gutschrift, Bescheid,
  Abrechnung, Schriftverkehr; an der
  Rechnung: Rechnung, Gutschrift).
- **Erkennung im Text** (Schrittart `RECOGNIZE`, Workflow ERP Schritt 030): Geschäftspartner und
  Vertrag aus dem erkannten Text über die **Erkennungsregeln** des Katalogs „Hinweise aus dem
  Dokument“ (Art „Erkennungsregel“: Werte eines Objects im Text – IBAN, Telefon, Kunden-,
  externe und Vertragsnummer, Name 1/2, Kürzel, Name laut Kontoauszug; Punkte je Treffer, Gruppe,
  „nur zusammen mit“). Ein Kandidat gilt ab `min_score` Punkten mit Abstand `min_gap` zum
  Nächstbesten; das Ergebnis ist ein **Vorschlag** für den Benutzerschritt (Partner 060,
  Vertrag 075), der Verlauf nennt die Gründe („Statler (P2) (IBAN, Kundennummer; 10 Punkte)“).
- **Ablauf mit ERP** (Workflow `ERP`): Text → Datum → Erkennung → Kategorie (050) → Partner (060,
  Vorschlag; „GP anlegen“ öffnet die Maske 070) → Vertrag bestätigen (075) → Rechnung aus Vorlage
  (080–090) → kaufmännisches Objekt (100) → Vertragsart (110) und Maske (120) → Übernahme (130)
  → Dokumentart (140) → Positionen übernehmen? (145) → Rechnung (150) → Ablage (160) → Datum (170)
  → Dokumentname (180) → ablegen (190). **Zurück** führt zur vorigen Frage und nimmt zurück, was
  sie und die Schritte danach gesetzt haben; **Von vorn** setzt das Dokument auf den Stand beim
  Einlesen und startet den Workflow neu; im Abschnitt **Ablauf** springt „Hierher zurück“ zu
  einer beantworteten Frage.
- **Kaufmännische Objekte je Kategorie** (`DmsCategoryObject`, Abschnitt an der Kategorie): Object,
  Bezeichnung, Filter, Feld des Partners, des Datums, des Titels, Positionen mit Sachkonto (z. B.
  `SupplierInvoiceItem` über `invoice_id`, Feld `account_number`), „anlegen“ erlaubt. Vorschläge:
  Vertrag → Vertrag; Rechnung / Gutschrift → Eingangsrechnung; Gebäude / Mietobjekt → Gebäude,
  Mietobjekt. Migration aus 0.2.x: Objekte je Dokumentart gehen an deren Kategorie.
- **Ablagefindung an der Ablage** (Ablagen im Buchungskreis, unterste Ebene): Sachkonten,
  Kostenarten und „umlagefähig“ (0.12.1). Je Position der Rechnung gilt die genaueste Ablage, alle
  gesetzten Merkmale müssen passen; Kostenart zählt 2, Sachkonto und „umlagefähig“ je 1 – z. B.
  Kostenart GARTEN → 5.2.1 vor Sachkonto 635001 → 5.1.2. Positionen ohne Treffer (Vorsteuer) zählen
  nicht; gleich genaue Treffer verschiedener Ablagen: Ablage wählen. Welche Felder der Positionen
  Kostenart und „umlagefähig“ tragen, steht am kaufmännischen Objekt der Kategorie. Im Workflow ERP
  fragt „Ablage wählen“ (160) immer, vorbelegt mit der ermittelten Ablage.
- **Ablage Ebene für Ebene** (Auswahl aus „Ablage Ebene für Ebene“, Ziel `file_account`):
  zuerst die oberste Ebene, eine Gruppe (▸) öffnet die nächste, „↑ zurück“ geht eine Ebene hoch;
  speichern lässt sich nur die unterste Ebene. „Suchen“ findet Ablagen über alle Ebenen.
- **Nächster Schritt:** Zwischen Kopfdaten und Dokument steht die Frage des wartenden
  Workflow-Schritts mit Nummer und Namen („Schritt 055 · Dokumentart wählen: …“, bei der
  Dokumentart mit der Kategorie) und ihrem Formular; nach dem Absenden erscheint dort der nächste Schritt
  (Kopfdaten aktualisiert). Ohne wartenden Schritt ist der Abschnitt ausgeblendet.
- **Dokument:** Kopfzeile, Aktionen (Weiter …, Ablegen, Bezug hinzufügen …, Text erkennen,
  Workflow neu starten …, Nach Fristablauf löschen) und das Dokument eingebettet (Viewer).
  **Fester Link:** `/file/DmsDocument/<id>` (auch mit dem Dokumentnamen). Suche auch im erkannten Text.
- **Dateiname beschreibt den Ablageort:** Muster `{account}:{company}:{property}_{object}_{date}_{doctype}:{partner}`,
  z. B. `52.0:VV:ROBad1_DL-2026-0003_20260302_VR:FGS.pdf`. Ablagenummer: die ersten zwei Ebenen
  zusammen, Punkt, der Rest zusammen (5.2.0 → 52.0, 5.2.3.4 → 52.34; am Buchungskreis einstellbar,
  mehrdeutige Nummern werden abgelehnt); `{property}` ist genau der Schlüssel der Objekt-Ebene
  (ohne Objekt das Ersatzverzeichnis „Allgemein“), der Bezug entfällt, wenn er das Objekt selbst ist; **Kürzel der Dokumentart**
  (`{doctype}`, eindeutig, z. B. RE, GS, VR, VG, BS, BB) und **Kürzel des Geschäftspartners**
  (Partner-Stammdaten, eindeutig; leer = Kurzname). Zwei Dokumente bekommen nie denselben Namen:
  belegt Datenbank oder Archiv ihn (auch gleichzeitig oder aus einem anderen Buchungskreis), gilt
  der nächste freie (`_2`, `_3` …).
- **Barbeleg:** Kategorie „Beleg (Barbeleg, Quittung)“, Objekt Vorerfassung (offen); Datum, Titel und
  Ablage (Sachkonto der Positionen, Verbindung `draft_id=id`) aus der Vorerfassung.
- **Sprachdaten:** Die Texterkennung nimmt die installierten Sprachen von Tesseract; fehlt eine
  gewünschte (z. B. deu), erkennt sie mit den vorhandenen und nennt es im Verlauf – Text gibt es
  auch dann, wenn die Datei unverändert bleibt.
- **Ablegen:** Ablage (unterste Ebene, im Buchungskreis aktiv), Dokumentart und -datum sind Pflicht.
  Der **Dokumentname** folgt dem Muster des Buchungskreises (Standard
  `{account}:{company}:{property}_{object}_{date}_{doctype}:{partner}`, z. B.
  `30.1:VV:ROBad1_20250110_BS:Stadt-Musterstadt.pdf`) und beschreibt mit der Nummer der Ablage
  den Ort: je Ebene ein Verzeichnis „Nummer Name“, darunter die **Objekt-Ebene** (Gebäude bzw.
  Wirtschaftseinheit des Dokuments – direkt, über Mietobjekt, Hauptobjekt des Vertrags oder Objekt
  der Rechnung; ohne Objekt „Allgemein“; am Buchungskreis abschaltbar). Nach dem Schreiben sind
  Datei (0444) und **Verzeichnisse (0555) nur lesbar** – darin lässt sich nichts löschen oder
  umbenennen; das DMS öffnet sie nur kurz zum Ablegen bzw. Löschen nach Frist. Beim Start werden
  vorhandene Archive gesperrt. Der Kopf ist fest; Löschen erst nach Ablauf der Frist. An jedem
  Bezug entsteht ein Verweis (Object `Documents`) mit Link auf das Dokument.
- **Vorentscheidung „verwerfen“** (Eingang mit `discard`): Dokumente warten dort ohne Workflow;
  „Endgültig verwerfen“ löscht die Datei im Eingang (Status „Verworfen“, erneut eingelesen
  bekannt), „An Eingang übergeben …“ holt sie zurück in die Bearbeitung.
- **Prüfungen** (je Buchungskreis einschaltbar): `ledger.posting` – Belegart mit Prüfregel braucht
  ein abgelegtes Dokument in den Ablagen der Regel, an der Vorerfassung oder an einem Datensatz,
  der auf sie zeigt (**Bezugsquellen**: Eingangsrechnung, Sollstellung eines Vertrags); beim Simulieren
  nur Warnung. `contract.activate` und `contract.condition` – z. B. Hausgeld `HG` am WEG-Vertrag nur
  mit einem Dokument, das nach dem Beginn der vorigen Zeitscheibe datiert.
- **Texterkennung:** Katalog **Befehle** (Einstellungen): je Zeile ein Aufruf ohne Shell,
  Platzhalter `{in}`, `{work}`, `{lang}` (Sprachen am Schritt), `{out}`; Ergebnis Text (Ausgabe der
  letzten Zeile), Datei ersetzen oder Variable; nur für passende Dateiarten. Mitgeliefert:
  `READ_TEXT`, `PDFTEXT`, `OCR_PDF`, `OCR_IMAGE`, `PREPARE_PDF`, `PREPARE_IMAGE`; die Schritte
  005–019 der mitgelieferten Workflows rufen sie auf, „Text vorhanden?“ (012) prüft die Zeichenzahl.
  Nur Programme aus `allowed_programs` (Plugin-Konfiguration) – auch beim Speichern eines Befehls.
  Der Host vererbt seine Umgebung nicht – `PATH` steht unter `env` in `configs/60-dms.yaml` (ohne
  PATH sucht das Plugin in /usr/local/bin, /usr/bin, /bin).
- **Eigene Scans** (Eingangsverzeichnis mit „Eigene Scans“): Ein Scan ohne Text-Ebene wird
  aufbereitet – Befehl `PREPARE_PDF` bzw. `PREPARE_IMAGE` schreibt `{out}`, das **ersetzt den Inhalt** (PDF/A mit
  Text-Ebene; der Viewer zeigt es, Text lässt sich markieren). Zugeschickte Scans (andere
  Eingangsverzeichnisse) und PDFs mit Text-Ebene bleiben unverändert. Doppelte Scans erkennt die
  Prüfsumme der eingelesenen Datei (`source_sha256`) auch nach der Aufbereitung. Das Original
  bleibt bis zur Ablage aufgehoben (`<Eingang>/.original/`).
- **An Eingang übergeben …:** Eine verirrte Datei (z. B. zugeschickter Scan im Scanner-Eingang)
  zieht in ein anderes Eingangsverzeichnis – war sie schon aufbereitet, mit dem Original; der
  erkannte Text wird verworfen, die Vorgaben des neuen Eingangs gelten, sein Workflow startet neu.
  Die Datei heißt dann `<Name>_step_over_from_<EINGANG>.<Endung>`; vorhandene Dateien werden nie
  überschrieben (sonst `_2`, `_3` …).
- **Objekt fehlt noch:** Die Auswahl der kaufmännischen Objekte zeigt Links „… anlegen“ auf das
  Formular „Neu“ im neuen Fenster (Objects der Kategorie mit „anlegen erlaubt“, weitere über
  Zuordnungen „Link“). Dort anlegen – noch nicht buchen bzw. aktivieren –, dann im Feld „Suchen“
  finden, wählen, ablegen. Beim Ablegen entsteht am Objekt der Verweis mit Dokumentname und Link.

### Workflows in Form 2 (seit 0.12.0)

Ein Schritt hat eine von acht Arten – Benutzer: **Eingabe** (Text, Datum, Zahl, Ja/Nein),
**Auswahl** (Liste eines Objects mit Filtern, feste Werte, Ablage Ebene für Ebene, kaufmännische
Objekte der Kategorie), **Maske** (Objekt anlegen, mit KI vorbelegt); Programm: **Befehl**,
**KI-Anfrage**, **Aktion** eines Objects, **Erkennen**, **Prüfen**. Alle lesen und schreiben Felder
des Dokuments, Bezüge (`ref`, `ref:<Object>`) und Variablen (`{var.<Name>}`); Programmschritte
machen Vorschläge oder setzen leere Felder, entschieden wird im Benutzerschritt. Statt freier
Parameter hat jede Art ihre Felder am Schritt (nur die passenden sind sichtbar), dazu:
- **Bedingungen:** „Schritt gilt nur, wenn“, „Prüfen“ (Ja, wenn alle erfüllt) und „Maske:
  Positionen aus der Vorbelegung, wenn“ – Wert (Platzhalter) Vergleich mit; Hinweis für den Verlauf.
- **Zuordnungen:** Filter (je Kategorie), feste Werte, Links, Parameter und Ergebnis einer Aktion,
  KI-Anfragen und Vorbelegung der Maske, Vorgaben und Werte aus dem Dokument.
- **Optionen:** Ergebnis → nächster Schritt; bei Benutzerschritten zugleich die **Knöpfe**
  (Text am Option, z. B. „GP anlegen“, „Ohne Partner weiter“). Pflichtauswahl ohne Vorauswahl.
- **Übersetzungen:** Frage, Kontext und Knöpfe je Sprache.
- **Prüfen** am Workflow meldet fehlende Folgeschritte, Ziele und Einstellungen; **Probelauf …**
  lässt den Workflow für die Dokumente im Eingang laufen (Transaktion wird zurückgerollt):
  Benutzerschritte nehmen den Vorschlag bzw. bisherigen Wert, ohne ihn endet der Lauf dort; die
  KI wird nicht gefragt, Aktionen mit „Ändert Daten“ laufen nicht.
- **Migration:** ERP und STANDARD entstehen neu aus der Vorlage; eigene Workflows werden aus ihren
  Parametern übernommen (ohne Entsprechung: Schritt inaktiv mit Hinweis). Wartende Dokumente
  bleiben am Schritt (ERP: frühere Bestätigung der Erkennung 040 → Partner 060) und bekommen ihren
  Ablauf.

### Objekte mit KI anlegen (seit 0.5.0)

Mit dem Plugin `ai` des Kerns legt der ERP-Workflow neue Objekte aus dem Dokument an: Die
Partnerauswahl (060) und die Auswahl des kaufmännischen Objekts (100) haben den Knopf
**Anlegen** (Option `CREATE`). Die **Maske** (070, 120, 150) schickt den erkannten Text an die
KI-Anfrage des Objects (Zuordnung „KI-Anfrage je Object“, nach der Kategorie), zeigt das Ergebnis
als Eingabemaske mit Vorgaben (z. B. Lieferant = Partner des Dokuments) und legt das Objekt mit
seinen Unter-Objekten an (Ziel `partner_id` bzw. `ref`). Die Antwort der KI bleibt am Dokument
gespeichert (auch über „Von vorn“); „KI neu fragen“ fragt erneut. Vor einem Vertrag fragt
Schritt 110 die Vertragsart (Variable `contract_type`, Platzhalter `{var.contract_type_label}` im
Kontext). `console dms:setup-ai` legt die Anfragen `BUSINESS_PARTNER`, `SUPPLIER_INVOICE`,
`SUPPLIER_CONTRACT` und `ECONOMIC_PLAN` an. Ist dem Dokument ein Vertrag zugeordnet, belegt die
Zuordnung „Vorbelegung“ (`SupplierInvoice.contractDraft`) die Rechnung aus dem Vertrag vor; die KI
geht bei den Feldern aus „KI geht vor“ vor. Schritt 145 fragt „Vertragspositionen übernehmen?“:
**Ja** – Positionen aus dem Vertrag, die KI liest mit `SUPPLIER_INVOICE_HEAD` nur den Kopf
(Bedingung „Positionen aus der Vorbelegung“); **Nein** – die KI liest die ganze Rechnung.
„Objekt mit KI anlegen …“ am Dokument bietet die Masken des Workflows auch nachträglich an.

Geschäftspartner anlegen (seit 0.10.0, Schritt 070) – auch wenn der Absender nicht im Text steht
(Briefkopf als Bild oder in Zierschrift):
- **Hinweise aus dem Dokument** (Katalog **Hinweise aus dem Dokument**, Einstellungen, seit 0.11.0):
  Jeder Eintrag ergibt den Platzhalter `{hint.<Schlüssel>}`. Es gibt zwei Arten (dazu die
  Erkennungsregeln, s. o.).
  **Muster im Text**: regulärer Ausdruck, Zeichen entfernen, Prüfung (z. B. IBAN), Werte aus
  Object.Feld ausschließen. **Wert aus einem anderen Object**: Quelle, Suche „Feld=Wert“ mit
  Platzhaltern, neuester zuerst, Feld mit dem Partner. Mitgeliefert sind `iban` (erste gültige,
  keine eigene – `BankAccount.iban`), `payee` (Name laut Kontoauszug, `BankTransaction`),
  `tax_number` und `vat_id`. Zeilen des Kontexts mit leerem Hinweis entfallen. Zuordnungen „Wert aus
  dem Dokument“ (z. B. `banks.iban` = `{hint.iban}`) füllen leere Felder der Maske, auch ohne KI.
- **Erkennung (030):** Die Punkte stehen an den Erkennungsregeln, Mindestpunkte und Abstand am
  Schritt; inaktive Regeln zählen nicht.
- **Vorschau** („Vor dem Anlegen Vorschau zeigen“): „Vorschau“ zeigt die Maske nur lesbar. Angelegt wird erst mit
  „Ja, so anlegen“; „Nein“ oder „Zurück“ führt zur Maske mit den Eingaben. Danach steht über dem
  nächsten Schritt „✔ Geschäftspartner … angelegt und am Dokument eingetragen“ (auch im Verlauf).
- **Datei an die KI auf Nachfrage** (Knopf „KI liest die Datei“, Dateiarten am Schritt, z. B.
  „application/pdf, image/*“): Der Knopf „KI liest die Datei“ schickt zum Text auch die Datei. Wie
  sie zum Dienst geht, steht am KI-Dienst („Dateien senden als“, ai 0.3.3).
- Ein Neustart des Workflows verwirft Variablen und den Stand der Masken; die gespeicherte Antwort
  der KI bleibt (0.12.0). Nach einem Fehler der KI fragt der Schritt beim nächsten Betreten neu.
- „Workflow neu starten …“ und „Von vorn“ setzen das Dokument auf den Stand beim Einlesen zurück
  (0.11.0): Kopf leer (außer den Feldern „Felder, die ‚Neu starten‘ … behalten“ am Workflow,
  Vorgabe `note`), Bezüge inaktiv, dann die Vorgaben des Eingangsverzeichnisses.
  Partner, Kategorie usw. werden neu gefragt.
- Partnerauswahl je Kategorie (0.11.0; seit 0.12.0 Zuordnungen „Filter“ mit Kategorie am Schritt 060,
  z. B. INVOICE: `role_kind` = CREDITOR, Beschreibung „nur Partner mit Kreditor-Rolle“). Bei Rechnungen und Belegen stehen nur
  Partner mit einer Rolle zur Wahl, deren Rollenart das Merkmal Kreditor hat (partner 0.14.1: Filter
  `role_kind` = CREDITOR bzw. DEBITOR). Welche Rollenarten zählen, steht im Katalog der
  Rollenarten. Weitere Filter als weitere Zuordnungen, z. B. `role` = WEGADM.

Am Dokument legt „Objekt mit KI anlegen …“ (seit 0.7.0) nachträglich an, was ein KI-Schritt des
Workflows anlegen würde – auch nach dem Ablegen (das Archiv bleibt unverändert, der Bezug kommt dazu).
Der ERP-Workflow geht in 10er-Schritten (seit 0.6.0); „Schritte neu nummerieren“ am Workflow
stellt jeden Workflow so um – mit Optionen, Bedingungen, Zuordnungen und den Dokumenten, die gerade darin stehen.

## Vorlagen (`template`)

Ausgaben aus Go-Templates für alle Buchungskreise: Briefe und Dokumente als PDF, HTML-Seiten,
XML und CSV. Menü **Vorlagen**: Aufträge, Vorlagen, Bundles, Aufbewahrung je Buchungskreis.

**Ablauf für andere Plugins**

1. `Template.render {template, language, company_code, data}` → `{job_number, status}`. Der
   Auftrag bekommt eine Nummer (Nummernkreis `TemplateJob`, `{CC}-{YYYY}-{N}`), die Daten stehen
   als YAML am Auftrag; gerechnet wird im Hintergrund.
2. SystemEvent `TemplateJob.status` mit `job_number`, `status` (`NEW`, `RUNNING`, `DONE`,
   `ERROR`) und `status_text`.
3. `Template.get {job_number}` → `{job_number, status, status_text, content_type, file_name,
   content}` (Inhalt Base64). Recht: `TemplateJob.render` bzw. Leserecht im Buchungskreis.

**Bundles** (Zip-Dateien, eigene Tabelle) gibt es in zwei Arten: Go-Templates und Binärdateien
(Bilder für PDF). Eine Vorlage hat ein Template-Bundle und für PDF höchstens ein Bundle mit
Binärdateien. Template-Bundles bilden Ketten: Die Vorlage nennt das konkreteste, `parent` das
nächst allgemeinere bis zum Bundle, das das Dokument aufbaut. Zusammengebaut wird in der
anderen Richtung – Blöcke (`block`) der allgemeinen ersetzt das konkrete mit `define`;
ausgeführt wird die erste Datei des allgemeinsten Bundles. In einem Bundle zählen die Dateien
nach Namen (alphanumerisch); für HTML nur `.html` (html/template, Werte maskiert), für XML nur
`.xml`, für CSV und PDF nur `.text` (text/template); alle anderen werden ignoriert.
Bundles lassen sich als Zip hoch- (ergänzt oder ersetzt) und herunterladen, in ein Verzeichnis
auf dem Server entpacken, dort bearbeiten und wieder einlesen (nur unter `allowed_dirs`) und
kopieren; Vorlagen ebenso kopieren (z. B. in eine andere Sprache).

**PDF**: Das Template erzeugt LaTeX (Begrenzer `<< >>`, damit LaTeX-Klammern nicht stören;
je Vorlage änderbar). Tectonic (XeTeX: UTF-8, Umlaute, Systemschriften) ist im Plugin
eingebettet (`tools/fetch-tectonic.sh` lädt es beim Bauen, mit Wiederholungen), wird in ein
temporäres Verzeichnis entpackt und übersetzt dort zusammen mit den Binärdateien. Fehlende
TeX-Pakete lädt Tectonic beim ersten Bedarf in `cache_dir`; scheitert der Download, wird der
Lauf wiederholt (`retries`). Bei Fehlern – mit Debug an der Vorlage immer – hängen Quelltext,
Protokoll und Hilfsdateien am Auftrag.

**Hilfsfunktionen**: Sprig v3 (ohne Zugriff auf Umgebung und Netz) und eigene: `tex`,
`texLines`, `xmlEscape`, `csvField`, `csvRow`, `fmtNumber`, `fmtMoney`, `fromMinor`, `sumBy`,
`fmtDate`, `fmtDateLong` (in der Sprache der Vorlage), `today`, `letters` (Briefe: Liste
`letters`, jeder erbt die übrigen Angaben; ohne Liste ein Brief), `image`, `job`, `lang`.

**Aufbewahrung**: Je Buchungskreis Tage für Aufträge mit Erfolg und mit Fehler (ohne Eintrag
7 bzw. 30, `keep_success_days`/`keep_error_days`); danach werden sie gelöscht, außer
„Nicht löschen“ ist gesetzt (bis dahin jederzeit möglich).

**Beispiele** („Beispiele anlegen …“ bzw. `console template:setup-examples --company=…`):
Kette `brief-beispiel → latex-brief → latex-basis` (Brief, Schleife über die Briefe),
`dokument-beispiel → latex-basis` (Wirtschaftsplan mit Kopf- und Fußzeile), `html-beispiel`
(Mieterliste) und `bilder-beispiel` (Logo); Vorlagen BRIEF, DOKUMENT, UEBERSICHT und je ein
Auftrag mit „Nicht löschen“. Dazu Standardvorlagen für Berichte (Bundle `latex-bericht`: Querformat,
Kopfzeile Name, Fußzeile Anschrift des Geschäftspartners zum Buchungskreis): Vertragsübersicht
(`VERTRAEGE`, `VERTRAEGE_XLS`) und Journal (`JOURNAL`, `JOURNAL_CSV`). Das Feld **Verwendung** einer
Vorlage bestimmt, in welchem Bericht sie zur Auswahl steht. Nach einem Release-Wechsel bringt
„Beispiele anlegen …“ mit „Vorhandene Beispiel-Bundles auf den Stand dieser Version bringen“ (bzw.
`console template:setup-examples --company=… --update=true`) die mitgelieferten Bundles auf den
neuen Stand; eigene Bundles und Vorlagen bleiben unberührt. `Template.render` mit `wait` wartet
höchstens so viele Sekunden auf das Ergebnis.

## Hauptbuch (`ledger`)

**Mengeneinheiten und Rundungsregeln (seit 0.25.0):** `Unit` (z. B. STK, H, M2, PAU; Nachkommastellen
der Menge) und `RoundingRule` – Art (kaufmännisch, Bankrundung, auf, ab), Schrittweite (0.01; 0.05 =
Rappenrundung) und Nachkommastellen des Einzelpreises (z. B. 4 für Energie). Standard: Regel des
Buchungskreises (`LedgerCompanyConfig.rounding_rule`), sonst die Regel mit „Standard“ (Vorschlag KFM:
kaufmännisch auf den Cent wie für Rechnung und Umsatzsteuer üblich). Konditionsart, Kondition,
nachgelagerte Art und Position können eine eigene Regel wählen (z. B. Material mit 4 Stellen).
Die Rechnung dazu liegt in `pkg/pricing` (Go) und `Billing.Pricing` (contract-billing).

Zentrales General Ledger. Fachmodule buchen **synchron und mehrdimensional** in ein
Universal Journal. Gebuchte Belege sind unveränderlich; Korrekturen sind Stornobelege.

### Datenmodell

Vollständiges DDL: [docs/ledger_schema_postgres.sql](docs/ledger_schema_postgres.sql).
Buchungsleitfaden private Vermietung (SKR0VV, Buchungskreis 2000) als Open Document Text in
Deutsch, Englisch und Chinesisch: [docs/buchungsleitfaden/](docs/buchungsleitfaden/) – erzeugt
mit `python3 docs/buchungsleitfaden/leitfaden.py` (Seitenzahlen im Inhalt über LibreOffice, falls installiert).
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

### Journal

Menü **Hauptbuch → Auswertungen → Journal**: Buchungskreis, Ledger, Zeitraum, Reihenfolge
(Buchungsdatum, Belegdatum, Erfassungszeitpunkt oder Belegnummer – der Zeitraum gilt für das
gewählte Datum) und Vorlage (Verwendung `JOURNAL`, Standard: `JOURNAL` PDF quer, `JOURNAL_CSV`).
Je Monat die Tage, darunter die Belege des Tages – das Datum steht einmal, je Beleg eine Kopfzeile
und die Positionen (Soll, Haben); Summen je Monat und gesamt. Die Kopfzeile jeder Seite nennt Monat
und Tag, an dem die Seite weitergeht.

### Bilanz (seit 0.27.0)

Die Gliederung steht im Katalog **Hauptbuch → Kontenplan → Bilanzschemata** (`BalanceSheetSchema`,
Posten `BalanceSheetItem`): je Posten Seite (Aktiva/Passiva), übergeordneter Posten (Summe),
Reihenfolge und Konten als Bereiche oder Nummern (`0200-0399, 1376`). Ein Konto gehört zum Bereich,
wenn seine ersten Stellen darin liegen; Unterkonten wie `315001` zählen so zu `3150`. Quelle
„Jahresergebnis“ nimmt den Saldo aller Erfolgskonten. „Vorschlag laden …“ legt für SKR04 das Schema
`HGB266K` an (verkürzt nach § 266 HGB, Saldenvorträge 9000–9099 im Eigenkapital); danach ist alles in
der Weboberfläche änderbar.

Gerechnet wird die Bilanz als Bericht in **Skripte und Berichte** (Modul script): `scripts/berichte.yaml`
enthält die Berichte `BILANZ` (Bilanz mit Vergleichsstichtag und Veränderung, wahlweise
Bewegungsbilanz – Mittelherkunft und -verwendung; Konten auf Wunsch; Abschnitt „Kontrolle“ für Konten
ohne Posten) und `MIETERLISTE` (alle Mieter im Zeitraum je Gebäude mit Tagen und Monaten, ohne Miete
und Nebenkosten). Einlesen über Skripte → „Importieren …“; Ausgabe PDF über die Vorlage `BERICHT`
(Vorlagen → „Beispiele einrichten“) und immer CSV. Die Salden kommen aus `AccountBalance.list` je
Geschäftsjahr bis zum Stichtag – der Vorjahresvergleich braucht den Saldovortrag ins neue Jahr.

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
console ledger:setup-company --company=2000 --chart=SKR0VV --currency=EUR --file=./skr0vv.json  # Vorschläge aus der Datei
console ledger:load-rates --file=./kurse.csv                   # rate_type;from_currency;to_currency;valid_from;rate[;from_factor;to_factor]
console ledger:periods --company=1000 --year=2026 --from=13 --to=16 --status=CLOSED
```

- **Hierarchie** (seit 0.14.0): Je Konto `parent_account` (übergeordnetes Konto) und `is_group`
  (Kontengruppe/-klasse). Gruppen helfen beim Suchen, gebucht wird nur auf der untersten Ebene:
  `setup-company` ordnet Gruppen keinem Buchungskreis zu, Zuordnung und Buchung lehnen sie ab.
  Das übergeordnete Konto muss es im Kontenplan geben und eine Gruppe sein.
- **Abstimmkonto und Feldstatus beim Einrichten:** Vorschläge je Konto aus `--file` (Format
  wie `load-coa`: `reconciliation_type`, `tax_category`, `field_status_group`), sonst aus dem
  mitgelieferten Kontenrahmen; ohne Vorschlag gilt die Kontoart im Kontenplan (Abstimmkonto
  Debitor/Kreditor/Anlagen aus `reconciliation_type` beim Laden) und der Feldstatus nach Kontotyp.
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
  - ordnet die **Grundausstattung** des Kontenplans zu (`--accounts=STARTER`, Standard, wenn
    der Kontenplan eine hat) oder alle aktiven Konten (`--accounts=ALL`, Standard ohne
    Grundausstattung), mit den Vorschlägen für Abstimmkonto und Steuerkategorie aus der
    Kontenrahmen-Datei,
  - öffnet die Perioden 1–12 des Jahres.
- **Kopieren** (seit 0.21.0): Aktion **„Kopieren …“** an gebuchten Belegen und an Vorerfassungen (auch
  gebuchten oder verworfenen) – neue Vorerfassung mit Belegart, Währung und allen Positionen (führendes
  Ledger) samt Kontierung; im Formular Buchungsdatum (Vorgabe Stichtag), Belegdatum, Kopftext und
  Referenz. Beträge und Kontierung danach in der Vorerfassung anpassen, z. B. für wiederkehrende
  Kleinbelege.
- **Partnersalden** (seit 0.20.0): Ist das Konto eines Debitors bzw. Kreditors ausgeglichen? Aktion
  **„Partnersalden …“** am Buchungskreis (Einstellungen → Buchungskreise) bzw.
  `console handle PartnerBalance.list --company_code=2000 [--date_to=…] [--open_only=true]`: je
  Partner und Abstimmkonto Soll, Haben, Saldo und Status (ausgeglichen, offene Forderung,
  offene Verbindlichkeit, Guthaben) bis zum Stichtag – aus den gebuchten Belegen, auf Wunsch mit
  den offenen Vorerfassungen (noch nicht gebuchte Sollstellungen).
- **Nur die Konten, die man braucht** (seit 0.19.0): Der Kontenplan bleibt vollständig, der
  Buchungskreis enthält nur die benötigten Konten.
  - **Grundausstattung:** Häkchen „Grundausstattung“ am Sachkonto des Kontenplans (oder
    `"is_starter": true` in der Datei von `load-coa`; fehlt das Feld, bleibt die Markierung
    unverändert). Gruppen gehören nie dazu.
  - **„Im Buchungskreis aktivieren …“** am Sachkonto des Kontenplans: ein weiteres Konto
    zuordnen, sobald man es braucht (z. B. Forderungen gegen den Geschäftsführer). Abschnitt
    „Aktiv in Buchungskreisen“ am Konto. Buchen oder Kontieren auf ein nicht aktiviertes Konto
    meldet, wo man es aktiviert.
  - **„Nicht benötigte Konten entfernen …“** am Buchungskreis (Einstellungen → Buchungskreise):
    entfernt Zuordnungen ohne Buchung und Vorerfassung, außerhalb der Grundausstattung und
    ohne Verwendung in einem Fachmodul (Partner, Verträge, Einkauf, Betriebskosten, Bank – gefragt
    über den Hook `ledger.chart_change`, Phase check). „Nur prüfen“ zeigt vorher die Liste.
    Die Konten bleiben im Kontenplan.
  - `change-chart` ordnet im neuen Kontenplan die Grundausstattung (bzw. alle) und jedes Ziel
    der Zuordnung zu.
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

### Rückwirkend buchen ab

In der Steuerung des Buchungskreises (`LedgerCompanyConfig.backdate_from`, seit 0.16.0) steht
das früheste erlaubte Buchungsdatum; ein Beleg davor wird abgelehnt (leer = keine Grenze, es
gelten nur die Periodensperren). Die Sollstellung (contract-billing 0.9.0) bucht
Nachberechnungen ab diesem Tag zu ihrer ursprünglichen Fälligkeit statt zum Lauftag – so lässt
sich ein Jahr vollständig nachbuchen bzw. simulieren.

### Unterkonto erzeugen

Aktion **„Unterkonto erzeugen …“** am Sachkonto (seit 0.18.0), z. B. je Bank ein eigenes Konto:
„gleiche Länge“ nimmt die nächste freie Nummer (1800 → 1801) neben dem Konto, „verlängert“
hängt zwei Stellen an (1800 → 180001, 180002 …) – das neue Konto hängt unter dem alten, das
alte wird Kontengruppe (nur ohne Buchungen; seine Zuordnungen in den Buchungskreisen werden
gesperrt). Eine eingegebene Nummer (z. B. 1821) geht vor. Das neue Konto übernimmt Kontotyp,
Kontoart und in jedem Buchungskreis des alten Kontos dessen Einstellungen (Währung,
Abstimmkonto, Steuerkategorie, Feldstatus).

### Kontenplan wechseln

`console ledger:change-chart --company=2000 --chart=SKR04 --mapping=./zuordnung.csv [--file=skr04.json] [--dry-run]`
(seit 0.17.0) – nur solange der Buchungskreis **keine Belege**, keine offene Vorerfassung und
keine Kontensperren hat. Die Zuordnung alte → neue Kontonummer (CSV `from;to` oder JSON
`{"600": "4861"}`) gilt für alle Module: Über den Hook `ledger.chart_change` prüfen Verträge,
Betriebskosten, Beschaffung, Bank und Geschäftspartner in der Phase check, ob jedes bei ihnen
gespeicherte Konto des Buchungskreises zugeordnet ist (sonst Meldung E mit den fehlenden
Konten), und stellen in commit um (`ledgerapi.ChartChangeHandler`). Das Hauptbuch tauscht
Kontenplan und Sachkonten des Buchungskreises; Abstimmkonto, Steuerkategorie und Feldstatus
der neuen Konten kommen wie bei setup-company aus `--file`. `--dry-run` prüft nur.

### Kontonummern und Kontoarten

- **Kontonummer = reine Nummer** (seit ledger 0.15.0), z. B. `1200` – in allen Tabellen:
  Kontenplan, Buchungskreis, Belege, Vorerfassung, Kontensperren und Fachmodule. Der Kontenplan
  ergibt sich aus dem Buchungskreis; gebucht wird immer auf Buchungskreis + Nummer. Eingaben mit
  dem Präfix des eigenen Kontenplans (`SKR25-1200`) werden gekürzt, ein fremder Kontenplan wird
  abgelehnt. Die Migration kürzt Altbestände aus 0.9.0–0.14.x im Hauptbuch und in den Fachmodulen
  (`ledgerapi.StripChartPrefix` im Migrator von contract, procurement, opcost, bank).
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
