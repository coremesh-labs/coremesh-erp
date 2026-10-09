#!/usr/bin/env python3
"""Buchungsleitfaden private Vermietung (SKR0VV) – erzeugt je Sprache ein ODT.

    python3 leitfaden.py            # schreibt Buchungsleitfaden_SKR0VV_<sprache>.odt hierher

Sprachen wie die Oberfläche: de, en, zh-CN. Kontobezeichnungen bleiben in allen
Fassungen die des Kontenplans SKR0VV (deutsch).
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from odtwriter import Document  # noqa: E402

LANGS = ["de", "en", "zh-CN"]
STAND = "2026-10-09"


def T(de, en, zh):
    return {"de": de, "en": en, "zh-CN": zh}


# --- Konten (Bezeichnung laut SKR0VV) -----------------------------------------------

ACC = {
    "2000": "Mietenkontokorrent - Sammelkonto", "2001": "Forderungen an ehemalige Mieter",
    "2740": "Bank", "2745": "Guthaben auf Sonderkonten", "271": "Kassenbestand",
    "159": "noch nicht abgerechnete andere Betriebskosten", "150": "noch nicht abgerechnete Beheizungs- und Warmwasserkosten",
    "001": "Gebäudekosten", "000": "Grundstückskosten", "3010": "Privateinlagen", "3011": "Privatentnahmen",
    "410": "Objektfinanzierungsmittel für das Anlagevermögen",
    "4311": "Anzahlungen auf noch nicht abgerechnete kalte Betriebskosten",
    "4312": "Anzahlungen auf noch nicht abgerechnete Warmwasser- und Heizkosten",
    "4400": "Mietüberzahlungen", "4401": "Überzahlungen aus der Umlagenabrechnung",
    "44202": "Verbindlichkeiten aus Instandhaltungsrechnungen", "44211": "Verbindlichkeiten aus sächlichen Verwaltungsaufwendungen",
    "44212": "Verbindlichkeiten aus der Hausbewirtschaftung", "44219": "Verbindlichkeiten aus sonstigen Lieferungen und Leistungen",
    "600": "Sollmieten", "6010": "Umlagen Warmwasser und Heizkosten", "6011": "Umlagen sonst. Betriebskosten",
    "602": "Gebühren und Zuschläge", "606": "Erlöse aus Sondereinrichtungen", "609": "Erlösschmälerungen",
    "66980": "Erstattungen Versicherungsbelastung", "66991": "Eingänge auf in früheren Jahren abgeschriebene Forderungen",
    "689": "andere Zinsen und ähnliche Erträge",
    "8000": "Kosten der Wasserversorgung", "8001": "Kosten der Entwässerung", "8002": "Kosten der Beheizung",
    "8003": "Kosten der Warmwasserversorgung", "8004": "Kosten für Aufzugsanlagen", "8005": "Kosten der Straßenreinigung",
    "8006": "Kosten der Müllabfuhr", "8007": "Kosten der Hausreinigung", "8008": "Kosten der Ungezieferbekämpfung",
    "8009": "Kosten der Gartenpflege", "8011": "Kosten für Beleuchtung", "8012": "Kosten für Schornsteinreinigung",
    "8013": "Kosten für Sach- und Haftpflichversicherung", "8014": "Kosten für fremde Hauswartleistungen",
    "8015": "Kosten des Betriebs von Gemeinschaftsantennen", "8017": "Kosten des Betriebs der maschinellen Wascheinrichtungen",
    "8019": "Grundsteuer", "8020": "Trinkwasseruntersuchung", "8021": "Wartungskosten", "8025": "Kosten Wartung Rauchwarnmelder",
    "8050": "Kosten der baulichen Instandhaltung", "8051": "Kosten für Schönheitsreparaturen",
    "8091": "Kosten für Miet- und Räumungsklagen", "821": "Fremdksoten für die Verwaltungsbetreuung",
    "8290": "Aufwendungen für Haushaltsnahe Dienstleistungen ohne Heizkosten",
    "85081": "Gerichts- und Anwaltskosten", "85082": "Prüfungs- und Beratungskosten", "8509": "Sonstige sächliche Verwaltungsaufwendungen",
    "8550": "Abschreibungen auf Forderungen", "872": "Zinsen auf Verbindlichkeiten gegenüber Kreditinstituten",
}


def acc(n):
    return f"{n} {ACC[n]}"


# Vorschläge für Konten, die SKR0VV nicht hat
NEW = {
    "4791": T("4791 Verbindlichkeiten aus Mietkautionen (ergänzt, Gruppe 479)",
              "4791 Liabilities from rent deposits (added, group 479)",
              "4791 租赁押金负债（已补充，科目组 479）"),
    "842": T("842 Abschreibungen auf Sachanlagen (ergänzt, Gruppe 84)",
             "842 Depreciation of tangible fixed assets (added, group 84)",
             "842 固定资产折旧（已补充，科目组 84）"),
    "2590": T("2590 Anteil Erhaltungsrücklage WEG (ergänzt, Gruppe 25)",
              "2590 Share of the WEG maintenance reserve (added, group 25)",
              "2590 业主共同体（WEG）维修基金份额（已补充，科目组 25）"),
}


def new(n, lang):
    return NEW[n][lang]


# --- Texte -----------------------------------------------------------------------------

TXT = {
    "title": T("Buchungsleitfaden private Vermietung", "Posting guide for private letting", "私人出租记账指南"),
    "subtitle": T("Kontenplan SKR0VV · Buchungskreis 2000 · CoreMesh ERP",
                  "Chart of accounts SKR0VV · company code 2000 · CoreMesh ERP",
                  "科目表 SKR0VV · 公司代码 2000 · CoreMesh ERP"),
    "stand": T(f"Stand: {STAND}. Erzeugt aus docs/buchungsleitfaden/leitfaden.py.",
               f"As of {STAND}. Generated from docs/buchungsleitfaden/leitfaden.py.",
               f"版本日期：{STAND}。由 docs/buchungsleitfaden/leitfaden.py 生成。"),
    "audience": T("Für Eigentümer, die ihre Objekte selbst buchen, und für Verwaltungen. Kontobezeichnungen stehen so im "
                  "Leitfaden, wie sie im Kontenplan SKR0VV geführt werden.",
                  "For owners who post their own properties and for property managers. Account names are quoted exactly as "
                  "they appear in the SKR0VV chart of accounts (German).",
                  "适用于自行记账的业主以及物业管理公司。科目名称按 SKR0VV 科目表中的原文（德文）引用。"),
    "toc": T("Inhalt", "Contents", "目录"),
    "footer": T("Buchungsleitfaden SKR0VV", "Posting guide SKR0VV", "SKR0VV 记账指南"),
    "debit": T("Soll", "Debit", "借方"), "credit": T("Haben", "Credit", "贷方"),
}


def amount(v, lang):
    """Betrag „1.250,00“ im Zahlenformat der Sprache (en, zh-CN: 1,250.00)."""
    return v if lang == "de" else v.replace(".", "\0").replace(",", ".").replace("\0", ",")


def build(lang):
    t = lambda d: d[lang]  # noqa: E731
    D = Document(lang, t(TXT["title"]), t(TXT["footer"]))
    D.title_page(t(TXT["title"]), t(TXT["subtitle"]), ["", t(TXT["audience"]), "", t(TXT["stand"])])
    D.toc(t(TXT["toc"]))
    S, H = t(TXT["debit"]), t(TXT["credit"])

    # 1 Grundsätze ----------------------------------------------------------------------
    D.h(1, t(T("1 Grundsätze", "1 Principles", "1 基本原则")))
    D.p(t(T(
        "Gebucht wird immer im **Buchungskreis** auf ein **Sachkonto des Buchungskreises**. Der Buchungskreis 2000 arbeitet "
        "mit dem Kontenplan SKR0VV; in allen Belegen und Einstellungen steht deshalb nur die **Kontonummer** (z. B. 600), "
        "nie der Kontenplan.",
        "Every posting is made in a **company code** to a **G/L account of that company code**. Company code 2000 uses "
        "the SKR0VV chart of accounts; documents and settings therefore only carry the **account number** (e.g. 600), "
        "never the chart.",
        "所有记账都在**公司代码**中记入**该公司代码的总账科目**。公司代码 2000 使用 SKR0VV 科目表，因此凭证和设置中只填写"
        "**科目编号**（例如 600），不填写科目表。")))
    D.bullets([t(x) for x in [
        T("**Nur die unterste Ebene ist bebuchbar.** Kontengruppen (z. B. 80 Aufwendungen für die Hausbewirtschaftung, "
          "800_804 Betriebskosten) dienen der Suche und Auswertung; das System lehnt Buchungen darauf ab.",
          "**Only the lowest level can be posted.** Account groups (e.g. 80, 800_804) are for searching and reporting; "
          "the system rejects postings to them.",
          "**只能记入最底层科目。** 科目组（如 80、800_804）仅用于查找和报表，系统拒绝记入科目组。"),
        T("**Fachmodule buchen selbst.** Sollstellung (Verträge), Eingangsrechnungen (Beschaffung), Kontoauszug (Bank) "
          "und Nebenkostenabrechnung (Betriebskosten) erzeugen ihre Belege über die **Vorerfassung**; dort werden sie "
          "geprüft und gebucht. Manuell gebucht wird nur, was kein Modul abdeckt.",
          "**Business modules post on their own.** Billing (contracts), supplier invoices (procurement), bank statements "
          "(bank) and service-charge settlements (operating costs) create their documents as **parked documents**, which "
          "are checked and posted there. Manual postings are only for what no module covers.",
          "**业务模块自行生成凭证。** 合同应收、供应商发票（采购）、银行对账单（银行）以及物业费结算（运营费用）均先生成"
          "**预制凭证**，经检查后过账。只有没有模块覆盖的业务才手工记账。"),
        T("**Kontierungsobjekte** (Mietobjekt, Vertrag, Gebäude, Kostenstelle …) ergänzen das Konto. Welche Pflicht, "
          "erlaubt oder ausgeblendet sind, bestimmt die **Feldstatusgruppe** des Kontos (Abschnitt 2.3).",
          "**Account assignment objects** (rental unit, contract, building, cost centre …) complement the account. "
          "Whether they are required, optional or suppressed is defined by the account's **field status group** (section 2.3).",
          "**辅助核算对象**（租赁单元、合同、楼栋、成本中心……）补充科目信息。是否必填、可选或隐藏由科目的**字段状态组**"
          "决定（见 2.3 节）。"),
        T("**Belegart** und **Positionsart** (Sachkonto, Debitor, Kreditor) prüft das Hauptbuch: Eine Debitorenzahlung (DZ) "
          "darf z. B. keine Kreditorenposition enthalten.",
          "The ledger checks **document type** and **item type** (G/L, customer, supplier): a customer payment (DZ), "
          "for example, may not contain a supplier item.",
          "总账检查**凭证类型**和**行项目类型**（总账、客户、供应商）：例如客户收款凭证（DZ）不得包含供应商行项目。"),
    ]])

    # 2 Einstellungen ------------------------------------------------------------------
    D.h(1, t(T("2 Konten und Kontierung", "2 Accounts and account assignment", "2 科目与辅助核算")))
    D.h(2, t(T("2.1 Die wichtigsten Konten", "2.1 Key accounts", "2.1 主要科目")))
    D.p(t(T("Auswahl der Konten des SKR0VV, die in der privaten Vermietung regelmäßig vorkommen. Feldstatus: Empfehlung "
            "(Abschnitt 2.3).",
            "Selection of SKR0VV accounts used regularly in private letting. Field status: recommendation (section 2.3).",
            "私人出租中经常使用的 SKR0VV 科目。字段状态为建议值（见 2.3 节）。")))
    head = [t(T("Konto", "Account", "科目")), t(T("Verwendung", "Use", "用途")), t(T("Feldstatus", "Field status", "字段状态"))]
    rows = [
        (acc("2000"), T("Abstimmkonto der Mieter (Debitoren). Sollstellung, Zahlungseingang, Abrechnungsergebnis.",
                         "Reconciliation account for tenants (customers): billing, payments, settlement result.",
                         "租户（客户）统驭科目：应收、收款、结算结果。"), "CUSTOMER"),
        (acc("2001"), T("Abstimmkonto für ausgezogene Mieter mit offenen Posten.",
                         "Reconciliation account for former tenants with open items.", "已退租且有未清项的租户统驭科目。"), "CUSTOMER"),
        (acc("2740"), T("Girokonto (Bankkonto im Modul Bank).", "Current account (bank account in the Bank module).",
                         "活期账户（银行模块中的银行账户）。"), "BANK"),
        (acc("2745"), T("Kautionskonto, Tagesgeld, Rücklagenkonto.", "Deposit account, savings, reserve account.",
                         "押金账户、活期存款、储备金账户。"), "BANK"),
        (acc("159"), T("Hausgeld-Vorauszahlungen an die WEG (Eigentumswohnung) bis zur Jahresabrechnung.",
                        "Service-charge advances paid to the owners' association (WEG) until its annual statement.",
                        "向业主共同体（WEG）预付的物业费，直至年度结算。"), "BALANCE"),
        (acc("4311"), T("Betriebskosten-Vorauszahlungen der Mieter (kalte Betriebskosten).",
                         "Tenants' advance payments for operating costs (cold).", "租户预付的运营费用（冷费用）。"), "BALANCE"),
        (acc("4312"), T("Heiz- und Warmwasser-Vorauszahlungen der Mieter.", "Tenants' advance payments for heating and hot water.",
                         "租户预付的供暖及热水费用。"), "BALANCE"),
        (acc("410"), T("Darlehen der Bank (Objektfinanzierung).", "Bank loan (property financing).", "银行贷款（物业融资）。"), "BALANCE"),
        (acc("44212"), T("Abstimmkonto Kreditoren Hausbewirtschaftung (Versorger, Dienstleister, WEG-Verwaltung).",
                          "Supplier reconciliation account for property operation (utilities, services, WEG manager).",
                          "物业运营供应商统驭科目（公用事业、服务商、WEG 管理人）。"), "SUPPLIER"),
        (acc("44202"), T("Abstimmkonto Kreditoren Instandhaltung (Handwerker).", "Supplier reconciliation account for repairs (tradesmen).",
                          "维修供应商统驭科目（工匠）。"), "SUPPLIER"),
        (acc("44219"), T("Abstimmkonto sonstige Kreditoren und Behörden (Grundsteuer).",
                          "Reconciliation account for other suppliers and authorities (property tax).",
                          "其他供应商及政府机关统驭科目（房产税）。"), "SUPPLIER"),
        (acc("600"), T("Kaltmiete (Sollmiete).", "Basic rent (rent receivable).", "基本租金（应收租金）。"), "RENT_REVENUE"),
        (acc("6010"), T("Abgerechnete Heiz- und Warmwasserkosten.", "Settled heating and hot-water costs.", "已结算的供暖及热水费用。"), "RENT_REVENUE"),
        (acc("6011"), T("Abgerechnete (kalte) Betriebskosten.", "Settled (cold) operating costs.", "已结算的（冷）运营费用。"), "RENT_REVENUE"),
        (acc("609"), T("Mietminderung, Nachlässe.", "Rent reduction, allowances.", "租金减免、折让。"), "RENT_REVENUE"),
        ("8000–8025", T("Umlagefähige Betriebskosten (Betriebskostenverordnung), je Kostenart ein Konto (Abschnitt 2.4).",
                         "Recoverable operating costs (German BetrKV), one account per cost type (section 2.4).",
                         "可分摊运营费用（德国《运营费用条例》），每种费用类型一个科目（见 2.4 节）。"), "STD"),
        (acc("8050"), T("Instandhaltung, Reparaturen (nicht umlagefähig).", "Maintenance, repairs (not recoverable).",
                         "维护、维修（不可分摊）。"), "STD"),
        (acc("821"), T("Verwaltergebühr (WEG- oder Mietverwaltung).", "Management fee (WEG or rental management).",
                        "管理费（WEG 或租赁管理）。"), "COST"),
        (acc("872"), T("Darlehenszinsen.", "Loan interest.", "贷款利息。"), "STD"),
        (acc("8509"), T("Kontoführung, Porto, sonstige Verwaltungskosten.", "Account fees, postage, other administrative costs.",
                         "账户费、邮费及其他管理费用。"), "COST"),
        (acc("3010") + " / 3011", T("Privateinlagen und -entnahmen des Eigentümers.", "Owner's private contributions and withdrawals.",
                                    "业主私人投入与提取。"), "BALANCE"),
    ]
    D.table(head, [(a, t(u), fs) for a, u, fs in rows], [4, 6, 1.6])

    D.h(2, t(T("2.2 Kontierungsobjekte", "2.2 Account assignment objects", "2.2 辅助核算对象")))
    D.p(t(T("Die Fachmodule geben ihre Objekte mit dem Beleg mit; das Hauptbuch schreibt sie nach dem Modul-Mapping des "
            "Buchungskreises (Hauptbuch → Steuerung, leer = Standard) in diese Felder der Buchungszeile:",
            "Business modules pass their objects with the document; the ledger writes them into these line fields according "
            "to the company code's module mapping (ledger → company settings, empty = standard):",
            "业务模块随凭证传递其对象；总账根据公司代码的模块映射（总账 → 控制设置，空 = 标准）写入以下行字段：")))
    D.table([t(T("Objekt", "Object", "对象")), t(T("Feld der Buchungszeile", "Line field", "凭证行字段")),
             t(T("Woher", "Source", "来源")), t(T("Wofür", "Purpose", "用途"))], [
        (t(T("Mietobjekt", "Rental unit", "租赁单元")), "rent_object_id",
         t(T("Vertrag (Sollstellung), Eingangsrechnung mit Objektart Mietobjekt, manuell",
             "Contract (billing), supplier invoice with object type rental unit, manual",
             "合同（应收）、对象类型为租赁单元的供应商发票、手工")),
         t(T("Erlöse und Kosten je Wohnung; Nebenkostenabrechnung", "Revenue and costs per flat; service-charge settlement",
             "按住房统计收入与成本；物业费结算"))),
        (t(T("Mietvertrag", "Rental contract", "租赁合同")), "rent_contract_id", t(T("Sollstellung, Kaution", "Billing, deposit", "应收、押金")),
         t(T("Offene Posten und Erlöse je Vertrag", "Open items and revenue per contract", "按合同的未清项与收入"))),
        (t(T("Gebäude", "Building", "楼栋")), "dimension_custom_1",
         t(T("Vertrag, Eingangsrechnung mit Objektart Gebäude", "Contract, supplier invoice with object type building",
             "合同、对象类型为楼栋的供应商发票")),
         t(T("Kosten, die das ganze Haus betreffen (Grundsteuer, Versicherung, Hausstrom)",
             "Costs concerning the whole house (property tax, insurance, common electricity)",
             "涉及整栋楼的费用（房产税、保险、公共用电）"))),
        (t(T("Wirtschaftseinheit", "Business entity", "经营单元")), "dimension_custom_2",
         t(T("Eingangsrechnung mit Objektart Wirtschaftseinheit", "Supplier invoice with object type business entity",
             "对象类型为经营单元的供应商发票")),
         t(T("Kosten einer ganzen Wohnanlage / WEG", "Costs of a whole estate / WEG", "整个住宅区 / WEG 的费用"))),
        (t(T("Mieter / Debitor", "Tenant / customer", "租户 / 客户")), "sd_customer_id",
         t(T("Sollstellung, Bank, Abrechnung", "Billing, bank, settlement", "应收、银行、结算")),
         t(T("Offene Posten des Mieters", "Tenant's open items", "租户的未清项"))),
        (t(T("Kreditor", "Supplier", "供应商")), "supplier_id", t(T("Eingangsrechnung, Bank", "Supplier invoice, bank", "供应商发票、银行")),
         t(T("Offene Posten des Lieferanten", "Supplier's open items", "供应商的未清项"))),
        (t(T("Kostenstelle", "Cost centre", "成本中心")), "cost_center", t(T("manuell, Eingangsrechnung", "manual, supplier invoice", "手工、供应商发票")),
         t(T("Optional: Verwaltungskosten, die keinem Objekt gehören (z. B. Büro, Fahrzeug)",
             "Optional: administrative costs not belonging to any property (e.g. office, vehicle)",
             "可选：不属于任何物业的管理费用（如办公室、车辆）"))),
    ], [2.2, 2.4, 4, 4.4])
    D.note(t(T(
        "**Faustregel:** Wohnungsbezogen → Mietobjekt. Hausbezogen → Gebäude. Anlagenbezogen (WEG) → Wirtschaftseinheit. "
        "Eine Kostenstelle braucht die private Vermietung meist nicht; sie ist nur für Kosten ohne Objektbezug gedacht.",
        "**Rule of thumb:** relates to a flat → rental unit. Relates to the house → building. Relates to the estate (WEG) → "
        "business entity. Private letting rarely needs cost centres; they are only meant for costs without property reference.",
        "**经验法则：** 与住房有关 → 租赁单元；与整栋楼有关 → 楼栋；与整个住宅区（WEG）有关 → 经营单元。私人出租通常不需要"
        "成本中心，它只用于与物业无关的费用。")))

    D.h(2, t(T("2.3 Feldstatusgruppen", "2.3 Field status groups", "2.3 字段状态组")))
    D.p(t(T("Die Feldstatusgruppe steht am Sachkonto im Buchungskreis (Hauptbuch → Sachkonten Buchungskreis). "
            "P = Pflicht, o = optional, – = ausgeblendet.",
            "The field status group is set on the G/L account in the company code (ledger → G/L accounts company code). "
            "R = required, o = optional, – = suppressed.",
            "字段状态组设置在公司代码的总账科目上（总账 → 公司代码总账科目）。必 = 必填，o = 可选，– = 隐藏。")))
    req = t(T("P", "R", "必"))
    fs = [("STD", "o", "o", "o", "o", "o"), ("BALANCE", "o", "o", "–", "–", "–"), ("BANK", "–", "–", "–", "–", "–"),
          ("CUSTOMER", "o", "o", "–", "–", "–"), ("SUPPLIER", "o", "–", "–", "–", "–"), ("RENT_REVENUE", "P", "P", "o", "–", "–"),
          ("REVENUE", "–", "–", "o", "o", "–"), ("OBJECT_COST", "P", "o", "o", "o", "o"), ("COST", "–", "–", "o", "o", "P"),
          ("TAX", "–", "–", "–", "–", "–")]
    names = {"STD": T("Standard (alle optional)", "Standard (all optional)", "标准（全部可选）"),
             "BALANCE": T("Bestandskonto", "Balance sheet account", "资产负债科目"),
             "BANK": T("Bank, Kasse, Treuhand", "Bank, cash, trust", "银行、现金、托管"),
             "CUSTOMER": T("Abstimmkonto Debitoren", "Customer reconciliation", "客户统驭科目"),
             "SUPPLIER": T("Abstimmkonto Kreditoren (Kreditor Pflicht)", "Supplier reconciliation (supplier required)", "供应商统驭科目（供应商必填）"),
             "RENT_REVENUE": T("Mieterlöse", "Rent revenue", "租金收入"), "REVENUE": T("Erlöse", "Revenue", "收入"),
             "OBJECT_COST": T("Objektaufwand", "Property expense", "物业费用"), "COST": T("Aufwand mit Kostenstelle", "Expense with cost centre", "带成本中心的费用"),
             "TAX": T("Steuerkonto", "Tax account", "税务科目")}
    D.table([t(T("Gruppe", "Group", "组")), t(T("Mietobjekt", "Rental unit", "租赁单元")), t(T("Vertrag", "Contract", "合同")),
             t(T("Gebäude", "Building", "楼栋")), t(T("Wirtschaftseinheit", "Business entity", "经营单元")), t(T("Kostenstelle", "Cost centre", "成本中心"))],
            [(f"{g} – {t(names[g])}",) + tuple(req if x == "P" else x for x in r) for g, *r in fs], [5, 1.4, 1.4, 1.4, 1.8, 1.6])
    D.note(t(T(
        "**Buchungskreis 2000 (eingerichtet):** Beim ersten Einrichten erhielten alle Aufwandskonten die Gruppe COST (Kostenstelle "
        "Pflicht) und alle Ertragskonten REVENUE. Für die Vermietung ist jetzt eingestellt: 600, 6010, 6011, 609 → RENT_REVENUE "
        "(Mietobjekt und Vertrag Pflicht); Betriebskosten 8000–8025, Instandhaltung 8050–8053, 8090–8099 und Zinsen 872 → STD "
        "(Mietobjekt, Gebäude oder Wirtschaftseinheit – je nach Kostenart); nur Verwaltungskosten 821, 85000–8509 → COST, "
        "falls Kostenstellen genutzt werden. Sonst scheitert z. B. eine Eingangsrechnung ohne Kostenstelle.",
        "**Company code 2000 (set up):** initially all expense accounts received group COST (cost centre "
        "required) and all revenue accounts REVENUE. Now set for letting: 600, 6010, 6011, 609 → RENT_REVENUE (rental "
        "unit and contract required); operating costs 8000–8025, maintenance 8050–8053, 8090–8099 and interest 872 → STD "
        "(rental unit, building or business entity depending on the cost type); only administrative costs 821, 85000–8509 → "
        "COST if cost centres are used. Otherwise e.g. a supplier invoice without cost centre is rejected.",
        "**公司代码 2000（已设置）：** 初始设置时所有费用科目均为 COST（成本中心必填），所有收入科目为 REVENUE。现已按出租业务"
        "设置为：600、6010、6011、609 → RENT_REVENUE（租赁单元和合同必填）；运营费用 8000–8025、维修 8050–8053、8090–8099 及利息 "
        "872 → STD（视费用类型填租赁单元、楼栋或经营单元）；仅当使用成本中心时，管理费用 821、85000–8509 → COST。否则，例如没有"
        "成本中心的供应商发票将被拒绝。")))

    D.h(2, t(T("2.4 Kostenarten und Konten", "2.4 Cost types and accounts", "2.4 费用类型与科目")))
    D.p(t(T("Die Kostenarten (Betriebskosten → Kostenarten) verbinden Eingangsrechnung, Vertragsabrechnung und "
            "Nebenkostenabrechnung mit dem Sachkonto. **In Buchungskreis 2000 so eingerichtet:**",
            "Cost types (operating costs → cost types) link supplier invoices, contract settlements and service-charge "
            "settlements to the G/L account. **In company code 2000 they are set up as follows:**",
            "费用类型（运营费用 → 费用类型）把供应商发票、合同结算和物业费结算与总账科目关联。**公司代码 2000 的费用类型尚未维护"
            "总账科目**——现已按下表设置：")))
    D.table([t(T("Kostenart", "Cost type", "费用类型")), "BetrKV", t(T("Konto", "Account", "科目")), t(T("Hinweis", "Note", "说明"))], [
        ("GRST Grundsteuer", "1", acc("8019"), t(T("8018 ist doppelt (ebenfalls Grundsteuer) – nicht verwenden", "8018 is a duplicate (also property tax) – do not use", "8018 重复（同为房产税）——勿用"))),
        ("WASSER", "2", acc("8000"), ""), ("ABWASSER", "3", acc("8001"), t(T("auch Niederschlagswasser", "incl. rainwater", "含雨水排放"))),
        ("HEIZUNG", "4", acc("8002"), t(T("Abrechnung über 4312 → 6010", "settled via 4312 → 6010", "通过 4312 → 6010 结算"))),
        ("WARMW", "5", acc("8003"), t(T("wie Heizung", "like heating", "同供暖"))), ("AUFZUG", "7", acc("8004"), ""),
        ("STRREIN", "8", acc("8005") + "\n\n" + acc("8006"), t(T("eine Kostenart, zwei Konten: Straßenreinigung bzw. Müll", "one cost type, two accounts: street cleaning or waste", "一个费用类型对应两个科目：街道清扫或垃圾"))),
        ("GEBREIN", "9", acc("8007") + "\n\n" + acc("8008"), ""), ("GARTEN", "10", acc("8009"), ""), ("BELEUCHT", "11", acc("8011"), t(T("Hausstrom", "common electricity", "公共用电"))),
        ("SCHORNST", "12", acc("8012"), ""), ("VERSICH", "13", acc("8013"), t(T("Gebäude-, Haftpflichtversicherung", "building and liability insurance", "楼宇险、责任险"))),
        ("HAUSWART", "14", acc("8014"), ""), ("ANTENNE", "15", acc("8015"), ""), ("WAESCHE", "16", acc("8017"), ""),
        ("SONSTBK", "17", acc("8021") + "\n\n" + acc("8020") + "\n\n" + acc("8025"), t(T("Wartung, Trinkwasser, Rauchwarnmelder", "maintenance, drinking water tests, smoke detectors", "维护、饮用水检测、烟雾报警器"))),
        ("VERWALT", "–", acc("821"), t(T("nicht umlagefähig", "not recoverable", "不可分摊"))),
        ("INSTAND", "–", acc("8050"), t(T("nicht umlagefähig; Schönheitsreparaturen 8051", "not recoverable; cosmetic repairs 8051", "不可分摊；装饰性维修 8051"))),
        ("KONTO", "–", acc("8509"), t(T("nicht umlagefähig", "not recoverable", "不可分摊"))),
        ("RUECKL", "–", new("2590", lang), t(T("Zuführung Erhaltungsrücklage (WEG) – Bilanzkonto", "contribution to the maintenance reserve (WEG) – balance sheet", "维修基金拨付（WEG）——资产负债科目"))),
    ], [2.6, 1.2, 5.2, 4.6])

    D.h(2, t(T("2.5 Kontenfindung der Verträge", "2.5 Contract account determination", "2.5 合同科目确定")))
    D.p(t(T("Verträge → Kontenfindung: je Vertragsart und Konditionsart das Konto der Gegenbuchung. Eingerichtet sind KM, NK, HK "
            "(Wohnraummiete) und GV (Grundsteuer); die übrigen Fälle des Leitfadens sind ergänzt:",
            "Contracts → account determination: the offsetting account per contract type and condition type. Set up are KM, NK, "
            "HK (residential lease) and GV (property tax); the other cases of this guide have been added:",
            "合同 → 科目确定：按合同类型和条件类型确定对方科目。原已设置 KM、NK、HK（住宅租赁）和 GV（房产税）；本指南其他情形现已补充：")))
    st = lambda done: t(T("eingerichtet", "set up", "已设置")) if done else t(T("ergänzt", "added", "已补充"))  # noqa: E731
    D.table([t(T("Vertragsart", "Contract type", "合同类型")), t(T("Konditionsart", "Condition type", "条件类型")),
             t(T("Konto", "Account", "科目")), t(T("Stand", "Status", "状态"))], [
        ("MV " + t(T("Wohnraummiete", "Residential lease", "住宅租赁")), "KM " + t(T("Kaltmiete", "Basic rent", "基本租金")), acc("600"), st(True)),
        ("MV", "NK " + t(T("Betriebskosten-Vorauszahlung", "Operating-cost advance", "运营费用预付")), acc("4311"), st(True)),
        ("MV", "HK " + t(T("Heizkosten-Vorauszahlung", "Heating advance", "供暖预付")), acc("4312"), st(True)),
        ("MV / SP", "ST " + t(T("Stellplatzmiete", "Parking rent", "车位租金")), acc("600"), st(False)),
        ("MV", "MM " + t(T("Mietminderung", "Rent reduction", "租金减免")), acc("609"), st(False)),
        ("MV", "MG " + t(T("Mahngebühr", "Dunning fee", "催款费")), acc("602"), st(False)),
        ("MV", "ZI " + t(T("Verzugszinsen", "Default interest", "逾期利息")), acc("689"), st(False)),
        ("KT " + t(T("Mietkaution", "Rent deposit", "租赁押金")), "KA " + t(T("Mietkaution", "Rent deposit", "租赁押金")), new("4791", lang), st(False)),
        ("GS " + t(T("Grundsteuer", "Property tax", "房产税")), "GV " + t(T("Grundsteuer-Vorauszahlung", "Property-tax instalment", "房产税预缴")), acc("8019"), st(True)),
        ("WH " + t(T("Hausgeld an WEG", "Service charge to WEG", "向 WEG 缴纳物业费")), "HV " + t(T("Hausgeld-Vorauszahlung", "Service-charge advance", "物业费预付")), acc("159"), st(False)),
        ("WH", "RZ " + t(T("Vorauszahlung Erhaltungsrücklage", "Maintenance-reserve advance", "维修基金预付")), new("2590", lang), st(False)),
        ("DA " + t(T("Darlehen (aufgenommen)", "Loan (taken)", "借入贷款")), "DZ " + t(T("Darlehenszinsen", "Loan interest", "贷款利息")), acc("872"), st(False)),
        ("DA", "DT / DS / AZ " + t(T("Tilgung, Sondertilgung, Auszahlung", "Repayment, special repayment, disbursement", "还本、提前还款、放款")), acc("410"), st(False)),
        ("BK " + t(T("Bankkonto", "Bank account", "银行账户")), "KF " + t(T("Kontoführungsentgelt", "Account fee", "账户管理费")), acc("8509"), st(False)),
        ("VS " + t(T("Versicherung", "Insurance", "保险")), "EN " + t(T("Prämie", "Premium", "保费")), acc("8013"), st(False)),
    ], [3.6, 4.4, 5.4, 1.8])

    # 3 Geschäftsvorfälle ---------------------------------------------------------------
    D.h(1, t(T("3 Geschäftsvorfälle", "3 Business transactions", "3 业务事项")))
    D.p(t(T("Je Vorfall: wo er erfasst wird, der Buchungssatz und die Kontierung. Beträge sind Beispiele.",
            "For each case: where it is entered, the posting record and the account assignment. Amounts are examples.",
            "每个事项说明：在何处录入、会计分录以及辅助核算。金额仅为示例。")))
    W = t(T("Erfassung", "Entry", "录入")) + ": "
    K = t(T("Kontierung", "Assignment", "辅助核算")) + ": "

    def case(title, where, lines, assign, note=None):
        D.h(2, t(title))
        D.p("**" + W + "**" + t(where))
        D.table([t(T("Soll/Haben", "Dr/Cr", "借/贷")), t(T("Konto", "Account", "科目")), t(T("Betrag (EUR)", "Amount (EUR)", "金额（EUR）"))],
                [(S if l[1] == "S" else H, l[0], amount(l[2], lang)) for l in lines], [2, 12, 3])
        D.p("**" + K + "**" + t(assign))
        if note:
            D.note(t(note))

    tenant = t(T("Mieter", "tenant", "租户"))
    case(T("3.1 Monatliche Sollstellung der Miete", "3.1 Monthly rent billing", "3.1 每月租金应收"),
         T("automatisch – Verträge → Sollstellung (contract-billing), Belegart DR. Die Vorerfassung bleibt offen, bis sie "
           "geprüft und gebucht ist (oder Vertragsart „automatisch buchen“).",
           "automatic – contracts → billing (contract-billing), document type DR. The parked document stays open until "
           "checked and posted (or contract type “post automatically”).",
           "自动——合同 → 应收（contract-billing），凭证类型 DR。预制凭证在检查并过账前保持未清（或合同类型设置为“自动过账”）。"),
         [(f"2000 ({tenant})", "S", "1.250,00"), (acc("600"), "H", "1.000,00"), (acc("4311"), "H", "150,00"), (acc("4312"), "H", "100,00")],
         T("Mietvertrag und Mietobjekt aus dem Vertrag (rent_contract_id, rent_object_id), Gebäude (dimension_custom_1), Mieter "
           "als Debitor. Fälligkeit aus der Kondition.",
           "Rental contract and unit from the contract (rent_contract_id, rent_object_id), building (dimension_custom_1), "
           "tenant as customer. Due date from the condition.",
           "租赁合同和租赁单元取自合同（rent_contract_id、rent_object_id），楼栋（dimension_custom_1），租户作为客户。到期日取自条件。"))
    case(T("3.2 Mieteingang auf dem Bankkonto", "3.2 Rent received on the bank account", "3.2 银行账户收到租金"),
         T("Bank → Kontoauszug einlesen, zuordnen (Regel, Verwendungszweck, IBAN, Name), buchen. Belegart DZ.",
           "Bank → import statement, match (rule, reference, IBAN, name), post. Document type DZ.",
           "银行 → 导入对账单，匹配（规则、用途、IBAN、名称），过账。凭证类型 DZ。"),
         [(acc("2740"), "S", "1.250,00"), (f"2000 ({tenant})", "H", "1.250,00")],
         T("Mieter (Debitor); der Ausgleich der offenen Posten folgt der Reihenfolge der Konditionsarten (Nebenforderungen zuerst).",
           "Tenant (customer); open items are cleared in the order of the condition types (secondary claims first).",
           "租户（客户）；未清项按条件类型顺序冲销（先冲从属债权）。"),
         T("Überzahlungen bleiben als Guthaben auf dem Debitor; am Jahresende ggf. auf 4400 Mietüberzahlungen umgliedern.",
           "Overpayments remain as a credit on the customer; at year end reclassify to 4400 if required.",
           "多付款项作为客户贷方余额保留；年末如有需要重分类至 4400。"))
    case(T("3.3 Mietminderung, Gutschrift", "3.3 Rent reduction, credit note", "3.3 租金减免、贷项通知"),
         T("Kondition MM am Vertrag (auch rückwirkend); der nächste Sollstellungslauf bucht die Differenz als Gutschrift (Belegart DG).",
           "Condition MM on the contract (also retroactive); the next billing run posts the difference as a credit note (document type DG).",
           "在合同上设置条件 MM（可追溯）；下一次应收运行将差额记为贷项通知（凭证类型 DG）。"),
         [(acc("609"), "S", "100,00"), (f"2000 ({tenant})", "H", "100,00")],
         T("wie Sollstellung (Vertrag, Mietobjekt).", "as for billing (contract, rental unit).", "同应收（合同、租赁单元）。"))
    case(T("3.4 Mahngebühr und Verzugszinsen", "3.4 Dunning fee and default interest", "3.4 催款费与逾期利息"),
         T("Kondition MG bzw. ZI (einmalig) am Mietvertrag.", "Condition MG or ZI (one-off) on the rental contract.", "在租赁合同上设置一次性条件 MG 或 ZI。"),
         [(f"2000 ({tenant})", "S", "5,00"), (acc("602") + " / 689", "H", "5,00")],
         T("Vertrag, Mietobjekt.", "Contract, rental unit.", "合同、租赁单元。"))
    case(T("3.5 Mietkaution", "3.5 Rent deposit", "3.5 租赁押金"),
         T("eigener Vertrag KT mit Bezugsvertrag (Mietvertrag), Kondition KA mit bis zu 3 Raten. Eingang über das Kautionskonto.",
           "separate contract KT referencing the rental contract, condition KA in up to 3 instalments. Received on the deposit account.",
           "单独的押金合同 KT（关联租赁合同），条件 KA 最多分 3 期。通过押金账户收款。"),
         [(f"2000 ({tenant})", "S", "3.000,00"), (new("4791", lang), "H", "3.000,00"),
          (acc("2745"), "S", "3.000,00"), (f"2000 ({tenant})", "H", "3.000,00")],
         T("Kautionsvertrag, Mietobjekt. Rückzahlung: 4791 an 2745; Verrechnung mit offenen Forderungen über den Debitor.",
           "Deposit contract, rental unit. Refund: 4791 to 2745; set-off against open receivables via the customer.",
           "押金合同、租赁单元。退还：借 4791 贷 2745；与未清应收款的抵销通过客户进行。"),
         T("Das Konto 4791 fehlte im SKR0VV und ist ergänzt (Gruppe 479, Feldstatus BALANCE). Die Kaution berührt die GuV nicht; Zinsen auf dem Kautionskonto stehen dem Mieter zu.",
           "Account 4791 was missing in SKR0VV and has been added (group 479, field status BALANCE). The deposit does not affect profit and loss; interest on the deposit account belongs to the tenant.",
           "SKR0VV 原无租赁押金科目，现已补充科目 4791（科目组 479，字段状态 BALANCE）。押金不影响损益；押金"
           "账户的利息归租户所有。"))
    sup = t(T("Kreditor", "supplier", "供应商"))
    case(T("3.6 Eingangsrechnung umlagefähige Betriebskosten", "3.6 Supplier invoice for recoverable operating costs", "3.6 可分摊运营费用的供应商发票"),
         T("Beschaffung → Eingangsrechnung (Rechnungsart ER, Belegart KR), Position mit Kostenart; das Konto kommt aus der Kostenart. "
           "Leistungszeitraum angeben – die Nebenkostenabrechnung verteilt danach.",
           "Procurement → supplier invoice (invoice type ER, document type KR), item with cost type; the account comes from the cost "
           "type. Enter the service period – the service-charge settlement allocates by it.",
           "采购 → 供应商发票（发票类型 ER，凭证类型 KR），行项目填写费用类型，科目取自费用类型。请填写服务期间——物业费结算据此分摊。"),
         [(acc("8006"), "S", "480,00"), (f"44212 ({sup})", "H", "480,00")],
         T("Objekt an der Position: Gebäude bei Hauskosten (→ dimension_custom_1), Mietobjekt bei Wohnungskosten (→ rent_object_id), "
           "Wirtschaftseinheit bei Anlagenkosten. Zahlung: Bank, Belegart KZ: 44212 an 2740.",
           "Object on the item: building for house costs (→ dimension_custom_1), rental unit for flat costs (→ rent_object_id), "
           "business entity for estate costs. Payment: bank, document type KZ: 44212 to 2740.",
           "行项目对象：整栋楼费用填楼栋（→ dimension_custom_1），住房费用填租赁单元（→ rent_object_id），住宅区费用填经营单元。"
           "付款：银行，凭证类型 KZ：借 44212 贷 2740。"),
         T("Umlagefähige Kosten am besten über die Eingangsrechnung erfassen: Die Nebenkostenabrechnung ordnet sie über das "
           "Objekt der Rechnung dem Haus zu. Manuelle Hauptbuchbuchungen ordnet sie über Mietobjekt, Gebäude "
           "(dimension_custom_1) oder Wirtschaftseinheit (dimension_custom_2) zu; ganz ohne Objekt gelten sie für jede "
           "Abrechnungseinheit, die das Konto sammelt.",
           "Best enter recoverable costs as supplier invoices: the settlement assigns them to the house via the invoice object. "
           "Manual ledger postings are assigned via rental unit, building (dimension_custom_1) or business entity "
           "(dimension_custom_2); without any object they count for every settlement unit collecting that account.",
           "可分摊费用最好通过供应商发票录入：结算会根据发票对象将其归属到楼栋。完全没有对象的手工总账记账，会计入所有归集该科目的"
           "结算单元。手工总账记账可通过租赁单元、楼栋（dimension_custom_1）或经营单元（dimension_custom_2）归属。"))
    case(T("3.7 Instandhaltung und Reparaturen", "3.7 Maintenance and repairs", "3.7 维护与维修"),
         T("Eingangsrechnung, Kostenart INSTAND (nicht umlagefähig).", "Supplier invoice, cost type INSTAND (not recoverable).", "供应商发票，费用类型 INSTAND（不可分摊）。"),
         [(acc("8050"), "S", "1.190,00"), (f"44202 ({sup})", "H", "1.190,00")],
         T("Mietobjekt (Wohnung) oder Gebäude. Handwerkerleistungen für haushaltsnahe Dienstleistungen ggf. zusätzlich auf 8290.",
           "Rental unit (flat) or building. Household-related services (tax relief) may go to 8290.",
           "租赁单元（住房）或楼栋。与家庭相关的服务（可抵税）可另记 8290。"),
         T("Größere Modernisierungen (Herstellungs- oder anschaffungsnahe Kosten) werden aktiviert: 001 Gebäudekosten statt 8050.",
           "Major modernisation (production or acquisition-related costs) is capitalised: 001 instead of 8050.",
           "较大规模的现代化改造（建造或购置相关费用）应资本化：记 001 而非 8050。"))
    auth = t(T("Behörde", "authority", "政府机关"))
    case(T("3.8 Grundsteuer", "3.8 Property tax", "3.8 房产税"),
         T("Vertrag GS mit der Gemeinde (Partnerrolle Behörde), Kondition GV vierteljährlich; Sollstellung über contract-billing.",
           "Contract GS with the municipality (partner role authority), condition GV quarterly; billing via contract-billing.",
           "与市政府签订合同 GS（合作伙伴角色：政府机关），条件 GV 按季度；通过 contract-billing 生成应付。"),
         [(acc("8019"), "S", "120,00"), (f"44219 ({auth})", "H", "120,00")],
         T("Gebäude bzw. Mietobjekt aus dem Vertrag. Der Grundsteuerbescheid kann als Vertragsabrechnung geprüft werden.",
           "Building or rental unit from the contract. The tax assessment can be checked as a contract settlement.",
           "楼栋或租赁单元取自合同。房产税通知可作为合同结算进行核对。"))
    case(T("3.9 Versicherung", "3.9 Insurance", "3.9 保险"),
         T("Vertrag VS mit Kondition EN (jährlich) oder Eingangsrechnung mit Kostenart VERSICH.",
           "Contract VS with condition EN (annual) or supplier invoice with cost type VERSICH.",
           "合同 VS 配条件 EN（按年），或使用费用类型 VERSICH 的供应商发票。"),
         [(acc("8013"), "S", "600,00"), (f"44212 ({sup})", "H", "600,00")],
         T("Gebäude. Versicherungsentschädigung: 2740 an 66980.", "Building. Insurance compensation: 2740 to 66980.", "楼栋。保险赔款：借 2740 贷 66980。"))
    weg = t(T("WEG-Verwaltung", "WEG manager", "WEG 管理人"))
    case(T("3.10 Hausgeld an die WEG (Eigentumswohnung)", "3.10 Service charge to the WEG (owned flat)", "3.10 向 WEG 缴纳物业费（自有公寓）"),
         T("Vertrag WH mit der WEG (Kreditor), Konditionen HV (Hausgeld) und RZ (Erhaltungsrücklage) monatlich.",
           "Contract WH with the WEG (supplier), conditions HV (service charge) and RZ (maintenance reserve) monthly.",
           "与 WEG 签订合同 WH（供应商），条件 HV（物业费）和 RZ（维修基金）按月。"),
         [(acc("159"), "S", "250,00"), (new("2590", lang), "S", "50,00"), (f"44212 ({weg})", "H", "300,00")],
         T("Mietobjekt (Wohnung), Wirtschaftseinheit. Zahlung per Lastschrift: Bank, KZ: 44212 an 2740.",
           "Rental unit (flat), business entity. Direct debit: bank, KZ: 44212 to 2740.", "租赁单元（住房）、经营单元。直接扣款：银行，KZ：借 44212 贷 2740。"))
    case(T("3.11 Jahresabrechnung der WEG", "3.11 Annual statement of the WEG", "3.11 WEG 年度结算"),
         T("Verträge → Vertragsabrechnung am WH-Vertrag: Positionen je Kostenart mit Gesamtkosten, Verteilerschlüssel (meist MEA) "
           "und Einzelbetrag; „Prüfen“, dann „Buchen …“. Beispiel: Kosten 2.900, Vorauszahlungen 3.000 – Guthaben 100.",
           "Contracts → contract settlement on the WH contract: items per cost type with total cost, allocation key (usually MEA) "
           "and individual amount; “check”, then “post …”. Example: costs 2,900, advances 3,000 – refund 100.",
           "合同 → 在 WH 合同上进行合同结算：按费用类型录入总费用、分摊键（通常为 MEA）及个人金额；先“检查”，再“过账……”。示例：费用 2,900，预付 3,000——退款 100。"),
         [("8000–8025 / 821 / 8050", "S", "2.900,00"), (f"44212 ({weg})", "S", "100,00"), (acc("159"), "H", "3.000,00")],
         T("Mietobjekt je Position. Umlagefähige Positionen stehen danach der Nebenkostenabrechnung der Mieter zur Verfügung "
           "(Quelle CONTRACT_SETTLEMENT). Die Differenz ist Nachzahlung (Haben) bzw. Guthaben (Soll) bei der WEG.",
           "Rental unit per item. Recoverable items are then available to the tenants' service-charge settlement (source "
           "CONTRACT_SETTLEMENT). The difference is an additional payment (credit) or refund (debit) with the WEG.",
           "每个行项目填写租赁单元。可分摊项目随后可用于租户物业费结算（来源 CONTRACT_SETTLEMENT）。差额为需向 WEG 补缴（贷方）或"
           "WEG 退款（借方）。"),
         T("Das Konto 2590 für den Anteil an der Erhaltungsrücklage ist im SKR0VV ergänzt. Verwaltergebühr (821) und "
           "Instandhaltung (8050) sind nicht umlagefähig.",
           "Account 2590 for the share of the maintenance reserve has been added to SKR0VV. Management fee (821) and "
           "maintenance (8050) are not recoverable.",
           "SKR0VV 中已补充维修基金份额科目 2590。管理费（821）和维修（8050）不可分摊。"))
    case(T("3.12 Nebenkostenabrechnung für die Mieter", "3.12 Service-charge settlement for tenants", "3.12 租户物业费结算"),
         T("Betriebskosten → Abrechnung: Definition NK (Vorauszahlungskonto 4311, Erlöskonto 6011) bzw. HK (4312, 6010); Regeln "
           "sammeln die Kosten (Hauptbuch, Eingangsrechnung, Vertragsabrechnung), verteilen nach Verteilerschlüssel (WFL, MEA, "
           "Personen …) zeitanteilig auf die Mietobjekte; Lauf rechnen, prüfen, buchen.",
           "Operating costs → settlement: definition NK (advance account 4311, revenue account 6011) or HK (4312, 6010); rules "
           "collect costs (ledger, supplier invoices, contract settlements) and distribute them pro rata by allocation key "
           "(living area, MEA, persons …) to the rental units; run, check, post.",
           "运营费用 → 结算：定义 NK（预付科目 4311，收入科目 6011）或 HK（4312、6010）；规则归集费用（总账、供应商发票、合同结算），"
           "按分摊键（居住面积、MEA、人数……）按时间比例分摊到租赁单元；运行、检查、过账。"),
         [(acc("4311"), "S", "1.800,00"), (f"2000 ({tenant})", "S", "150,00"), (acc("6011"), "H", "1.950,00")],
         T("Mietvertrag und Mietobjekt je Mieter. Nachzahlung: Soll Debitor (Beispiel). Guthaben: Haben Debitor (Auszahlung "
           "oder Verrechnung mit der nächsten Miete).",
           "Rental contract and unit per tenant. Additional payment: debit customer (example). Refund: credit customer "
           "(pay out or offset against the next rent).",
           "每个租户填写租赁合同和租赁单元。补缴：借记客户（示例）。退款：贷记客户（支付或与下期租金抵销）。"),
         T("Die Kosten bleiben auf 8000–8025; 6011 zeigt die umgelegten Beträge. Den nicht umlegbaren Rest (Leerstand, "
           "Eigennutzung) trägt der Eigentümer. Abrechnung des Jahres X erfolgt im Jahr X+1.",
           "Costs remain on 8000–8025; 6011 shows the recharged amounts. The non-recoverable remainder (vacancy, own use) is "
           "borne by the owner. The settlement for year X is made in year X+1.",
           "费用保留在 8000–8025；6011 反映已分摊金额。不可分摊的剩余部分（空置、自用）由业主承担。X 年的结算在 X+1 年进行。"))
    bank = t(T("Bank", "bank", "银行"))
    case(T("3.13 Darlehen: Auszahlung, Zins und Tilgung", "3.13 Loan: disbursement, interest and repayment", "3.13 贷款：放款、利息与还本"),
         T("Vertrag DA mit der Bank (Kreditor) und Darlehenskondition (Betrag, Zins, Rate, Tilgungsart); Tilgungsplan und "
           "Sollstellung rechnet contract-billing.",
           "Contract DA with the bank (supplier) and loan condition (amount, interest, instalment, repayment type); "
           "contract-billing computes the schedule and billing.",
           "与银行签订合同 DA（供应商）并设置贷款条件（金额、利率、分期、还款方式）；还款计划和应付由 contract-billing 计算。"),
         [(acc("872"), "S", "410,00"), (acc("410"), "S", "590,00"), (f"44219 ({bank})", "H", "1.000,00")],
         T("Gebäude bzw. Mietobjekt der Finanzierung. Auszahlung (AZ): 2740 an 410. Rate per Lastschrift: Bank, KZ.",
           "Building or rental unit financed. Disbursement (AZ): 2740 to 410. Instalment by direct debit: bank, KZ.",
           "被融资的楼栋或租赁单元。放款（AZ）：借 2740 贷 410。分期扣款：银行，KZ。"))
    case(T("3.14 Bankgebühren und Zinserträge", "3.14 Bank charges and interest income", "3.14 银行手续费与利息收入"),
         T("Bank → Kontoauszug; einmal eine Regel „an Sachkonto“ lernen, dann ordnet die Bank selbst zu. Belegart SA.",
           "Bank → statement; learn a rule “to G/L account” once, then the bank matches automatically. Document type SA.",
           "银行 → 对账单；学习一次“记入总账科目”的规则后，银行将自动匹配。凭证类型 SA。"),
         [(acc("8509"), "S", "9,90"), (acc("2740"), "H", "9,90")],
         T("Kostenstelle nur, wenn Feldstatus COST. Zinsgutschrift: 2740 an 689.", "Cost centre only if field status COST. Interest credit: 2740 to 689.",
           "仅当字段状态为 COST 时填写成本中心。利息入账：借 2740 贷 689。"))
    case(T("3.15 Abschreibung des Gebäudes", "3.15 Depreciation of the building", "3.15 楼宇折旧"),
         T("jährlich manuell im Hauptbuch (Vorerfassung, Belegart SA) – ein Anlagenmodul gibt es noch nicht.",
           "annually by hand in the ledger (parked document, document type SA) – there is no asset module yet.",
           "每年在总账中手工录入（预制凭证，凭证类型 SA）——目前尚无固定资产模块。"),
         [(new("842", lang), "S", "4.000,00"), (acc("001"), "H", "4.000,00")],
         T("Gebäude (dimension_custom_1). Grund und Boden (000) wird nicht abgeschrieben.",
           "Building (dimension_custom_1). Land (000) is not depreciated.", "楼栋（dimension_custom_1）。土地（000）不计提折旧。"),
         T("SKR0VV hatte nur 841 Sonderabschreibungen auf Sachanlagen – für die planmäßige Abschreibung ist Konto 842 ergänzt.",
           "SKR0VV only had 841 special depreciation – account 842 has been added for scheduled depreciation.",
           "SKR0VV 原只有 841（特别折旧）——现已补充科目 842 用于计划折旧。"))
    case(T("3.16 Forderungsausfall und Mieterwechsel", "3.16 Bad debt and change of tenant", "3.16 坏账与租户更换"),
         T("Vorerfassung, Belegart AB (Verrechnung). Ausgezogene Mieter mit offenen Posten führt man auf 2001.",
           "Parked document, document type AB (clearing). Former tenants with open items are kept on 2001.",
           "预制凭证，凭证类型 AB（清账）。已退租且有未清项的租户记入 2001。"),
         [(acc("8550"), "S", "800,00"), (f"2000 / 2001 ({tenant})", "H", "800,00")],
         T("Mietvertrag, Mietobjekt. Spätere Zahlung auf abgeschriebene Forderungen: 2740 an 66991. Räumungsklage, Anwalt: 8091 bzw. 85081.",
           "Rental contract, unit. Later payment on written-off receivables: 2740 to 66991. Eviction, lawyer: 8091 or 85081.",
           "租赁合同、租赁单元。已核销应收款的后续收款：借 2740 贷 66991。驱逐诉讼、律师费：8091 或 85081。"))
    case(T("3.17 Privateinlagen und -entnahmen", "3.17 Private contributions and withdrawals", "3.17 私人投入与提取"),
         T("Vorerfassung oder Bankregel, Belegart SA.", "Parked document or bank rule, document type SA.", "预制凭证或银行规则，凭证类型 SA。"),
         [(acc("2740"), "S", "5.000,00"), (acc("3010"), "H", "5.000,00"), (acc("3011"), "S", "2.000,00"), (acc("2740"), "H", "2.000,00")],
         T("keine.", "none.", "无。"),
         T("Für Eigentümer, die die Vermietung privat führen: Ausgaben vom Privatkonto werden als Einlage gebucht "
           "(Aufwand an 3010).", "For owners letting privately: expenses paid from a private account are posted as a "
           "contribution (expense to 3010).", "对私人经营出租的业主：从私人账户支付的费用记为投入（借费用，贷 3010）。"))

    # 4 Lücken ------------------------------------------------------------------------
    D.h(1, t(T("4 Stand der Einrichtung in Buchungskreis 2000", "4 Set-up status in company code 2000", "4 公司代码 2000 的设置状态")))
    done, todo = t(T("eingerichtet", "set up", "已设置")), t(T("offen", "open", "待办"))
    D.table([t(T("Was", "What", "内容")), t(T("Abschnitt", "Section", "章节")), t(T("Wo", "Where", "位置")), t(T("Stand", "Status", "状态"))], [
        (t(T("Konten 4791, 842, 2590 im SKR0VV", "Accounts 4791, 842, 2590 in SKR0VV", "SKR0VV 中的科目 4791、842、2590")), "3.5, 3.10, 3.15",
         t(T("Hauptbuch → Sachkonten, Sachkonten Buchungskreis", "Ledger → G/L accounts, company code accounts", "总账 → 总账科目、公司代码科目")), done),
        (t(T("Feldstatus der Erlös- und Aufwandskonten", "Field status of revenue and expense accounts", "收入与费用科目的字段状态")), "2.3",
         t(T("Hauptbuch → Sachkonten Buchungskreis", "Ledger → company code accounts", "总账 → 公司代码科目")), done),
        (t(T("Sachkonten der Kostenarten", "G/L accounts of the cost types", "费用类型的总账科目")), "2.4",
         t(T("Betriebskosten → Kostenarten", "Operating costs → cost types", "运营费用 → 费用类型")), done),
        (t(T("Kontenfindung der Verträge", "Contract account determination", "合同科目确定")), "2.5",
         t(T("Verträge → Kontenfindung", "Contracts → account determination", "合同 → 科目确定")), done),
        (t(T("Rückwirkend buchen ab 01.01.2026", "Back-dated posting from 2026-01-01", "自 2026-01-01 起允许追溯记账")), "3.1",
         t(T("Hauptbuch → Buchungskreise", "Ledger → company codes", "总账 → 公司代码")), done),
        (t(T("Abrechnungsdefinitionen NK und HK je Wirtschaftseinheit", "Settlement definitions NK and HK per business entity", "按经营单元的结算定义 NK 与 HK")), "3.12",
         t(T("Betriebskosten → Abrechnungsdefinitionen", "Operating costs → settlement definitions", "运营费用 → 结算定义")),
         done + " – " + t(T("Regeln offen", "rules open", "规则待定"))),
        (t(T("Bankkonto im Buchungskreis 2000", "Bank account in company code 2000", "公司代码 2000 的银行账户")), "3.2",
         t(T("Bank → Bankkonten (Sachkonto 2740)", "Bank → bank accounts (G/L account 2740)", "银行 → 银行账户（总账科目 2740）")), todo),
    ], [6, 2.2, 5.5, 3])
    D.p(t(T("**Rückwirkend buchen ab** (Hauptbuch → Buchungskreise): frühestes erlaubtes Buchungsdatum. Die Sollstellung bucht "
            "Nachberechnungen ab diesem Tag zu ihrer ursprünglichen Fälligkeit statt zum Tag des Laufs – so lässt sich ein ganzes "
            "Jahr nachträglich durchbuchen.",
            "**Back-dated posting from** (ledger → company codes): earliest permitted posting date. From this date, billing posts "
            "corrections at their original due date instead of the run date – so a whole year can be posted afterwards.",
            "**允许追溯记账起始日**（总账 → 公司代码）：最早允许的过账日期。自该日起，应收调整按原到期日而非运行日过账——"
            "从而可以事后完整记入整年业务。")))
    D.p(t(T("Die Nummern der ergänzten Konten liegen in der passenden Kontengruppe; jede freie Nummer der untersten Ebene "
            "wäre ebenso möglich.",
            "The numbers of the added accounts lie within the matching account group; any free number at the lowest level "
            "would work as well.",
            "补充科目的编号位于相应科目组内；也可使用任何空闲的最底层编号。")))
    return D


def page_numbers(odt, headings):
    """Seiten der Überschriften aus einem Rendering mit LibreOffice (falls vorhanden)."""
    import shutil
    import subprocess
    import tempfile
    office = shutil.which("libreoffice") or shutil.which("soffice")
    if not office or not shutil.which("pdftotext"):
        return {}
    out = tempfile.mkdtemp(dir=os.path.dirname(odt))
    try:
        subprocess.run([office, "--headless", "--convert-to", "pdf", "--outdir", out, odt], capture_output=True, timeout=300)
        pdf = os.path.join(out, os.path.basename(odt)[:-4] + ".pdf")
        text = subprocess.run(["pdftotext", "-layout", pdf, "-"], capture_output=True, text=True).stdout
    finally:
        shutil.rmtree(out, ignore_errors=True)
    pages, found = text.split("\f"), {}
    for _, h in headings:
        for i, pg in enumerate(pages[2:], start=3):  # Titel und Inhalt überspringen
            if any(line.strip() == h for line in pg.splitlines()):
                found[h] = str(i)
                break
    return found


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    for lang in LANGS:
        path = os.path.join(here, f"Buchungsleitfaden_SKR0VV_{lang}.odt")
        doc = build(lang)
        doc.save(path)
        doc.pages = page_numbers(path, doc.headings)  # zweiter Durchgang: Seitenzahlen im Inhaltsverzeichnis
        doc.save(path)
        print("geschrieben:", path, f"({len(doc.pages)}/{len(doc.headings)} Seitenzahlen)")


if __name__ == "__main__":
    main()
