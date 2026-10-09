"""Kleiner ODF-Textschreiber (Open Document Text, .odt) ohne Fremdbibliotheken.

Unterstützt: Titel, Überschriften (Ebene 1–3), Absätze mit **fett**, Aufzählungen,
Tabellen mit Kopfzeile, Hinweiskästen und ein Inhaltsverzeichnis-Feld.
"""

import re
import zipfile
from xml.sax.saxutils import escape

NS = (
    'xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" '
    'xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0" '
    'xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" '
    'xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" '
    'xmlns:fo="urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0" '
    'xmlns:svg="urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0" '
    'xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0" '
    'xmlns:dc="http://purl.org/dc/elements/1.1/" '
    'xmlns:loext="urn:org:documentfoundation:names:experimental:office:xmlns:loext:1.0" '
    'office:version="1.3"'
)

FONT = "Liberation Sans"
FONT_ASIAN = "Noto Sans CJK SC"

STYLES = f"""<?xml version="1.0" encoding="UTF-8"?>
<office:document-styles {NS}>
 <office:font-face-decls>
  <style:font-face style:name="{FONT}" svg:font-family="'{FONT}'" style:font-family-generic="swiss"/>
  <style:font-face style:name="{FONT_ASIAN}" svg:font-family="'{FONT_ASIAN}'" style:font-family-generic="swiss"/>
 </office:font-face-decls>
 <office:styles>
  <style:default-style style:family="paragraph">
   <style:paragraph-properties fo:margin-top="0cm" fo:margin-bottom="0.18cm" fo:line-height="120%"/>
   <style:text-properties style:font-name="{FONT}" fo:font-size="10pt" style:font-name-asian="{FONT_ASIAN}"
     style:font-size-asian="10pt" fo:language="de" fo:country="DE"/>
  </style:default-style>
  <style:style style:name="Standard" style:family="paragraph"/>
  <style:style style:name="Body" style:family="paragraph" style:parent-style-name="Standard">
   <style:paragraph-properties fo:text-align="start"/>
  </style:style>
  <style:style style:name="Title" style:family="paragraph" style:parent-style-name="Standard">
   <style:paragraph-properties fo:margin-top="3cm" fo:margin-bottom="0.4cm"/>
   <style:text-properties fo:font-size="24pt" fo:font-weight="bold" style:font-size-asian="24pt" style:font-weight-asian="bold" fo:color="#1f3a5f"/>
  </style:style>
  <style:style style:name="Subtitle" style:family="paragraph" style:parent-style-name="Standard">
   <style:paragraph-properties fo:margin-bottom="0.2cm"/>
   <style:text-properties fo:font-size="13pt" style:font-size-asian="13pt" fo:color="#4a5568"/>
  </style:style>
  <style:style style:name="Heading_20_1" style:display-name="Heading 1" style:family="paragraph" style:parent-style-name="Standard"
    style:default-outline-level="1">
   <style:paragraph-properties fo:margin-top="0.6cm" fo:margin-bottom="0.25cm" fo:keep-with-next="always" fo:break-before="page"/>
   <style:text-properties fo:font-size="16pt" fo:font-weight="bold" style:font-size-asian="16pt" style:font-weight-asian="bold" fo:color="#1f3a5f"/>
  </style:style>
  <style:style style:name="Heading_20_2" style:display-name="Heading 2" style:family="paragraph" style:parent-style-name="Standard"
    style:default-outline-level="2">
   <style:paragraph-properties fo:margin-top="0.45cm" fo:margin-bottom="0.15cm" fo:keep-with-next="always"/>
   <style:text-properties fo:font-size="13pt" fo:font-weight="bold" style:font-size-asian="13pt" style:font-weight-asian="bold" fo:color="#1f3a5f"/>
  </style:style>
  <style:style style:name="Heading_20_3" style:display-name="Heading 3" style:family="paragraph" style:parent-style-name="Standard"
    style:default-outline-level="3">
   <style:paragraph-properties fo:margin-top="0.3cm" fo:margin-bottom="0.1cm" fo:keep-with-next="always"/>
   <style:text-properties fo:font-size="11pt" fo:font-weight="bold" style:font-size-asian="11pt" style:font-weight-asian="bold"/>
  </style:style>
  <style:style style:name="TableHead" style:family="paragraph" style:parent-style-name="Standard">
   <style:paragraph-properties fo:margin-bottom="0cm"/>
   <style:text-properties fo:font-size="9pt" fo:font-weight="bold" style:font-size-asian="9pt" style:font-weight-asian="bold" fo:color="#ffffff"/>
  </style:style>
  <style:style style:name="TableText" style:family="paragraph" style:parent-style-name="Standard">
   <style:paragraph-properties fo:margin-bottom="0cm"/>
   <style:text-properties fo:font-size="9pt" style:font-size-asian="9pt"/>
  </style:style>
  <style:style style:name="Note" style:family="paragraph" style:parent-style-name="Standard">
   <style:paragraph-properties fo:margin-top="0.1cm" fo:margin-bottom="0.25cm" fo:padding="0.2cm" fo:background-color="#fff7e0"
     fo:border-left="0.08cm solid #d69e2e"/>
  </style:style>
  <style:style style:name="Booking" style:family="paragraph" style:parent-style-name="Standard">
   <style:paragraph-properties fo:margin-left="0.4cm" fo:margin-bottom="0.1cm"/>
   <style:text-properties style:font-name="Liberation Mono" fo:font-size="9pt" style:font-size-asian="9pt"/>
  </style:style>
  <style:style style:name="Strong" style:family="text">
   <style:text-properties fo:font-weight="bold" style:font-weight-asian="bold"/>
  </style:style>
  <style:style style:name="Contents_20_Heading" style:display-name="Contents Heading" style:family="paragraph" style:parent-style-name="Standard">
   <style:paragraph-properties fo:break-before="page" fo:margin-bottom="0.3cm"/>
   <style:text-properties fo:font-size="14pt" fo:font-weight="bold" style:font-size-asian="14pt" style:font-weight-asian="bold"/>
  </style:style>
  <style:style style:name="Contents_20_1" style:display-name="Contents 1" style:family="paragraph" style:parent-style-name="Standard">
   <style:paragraph-properties>
    <style:tab-stops><style:tab-stop style:position="17cm" style:type="right" style:leader-style="dotted" style:leader-text="."/></style:tab-stops>
   </style:paragraph-properties>
  </style:style>
  <style:style style:name="Contents_20_2" style:display-name="Contents 2" style:family="paragraph" style:parent-style-name="Standard">
   <style:paragraph-properties fo:margin-left="0.5cm">
    <style:tab-stops><style:tab-stop style:position="16.5cm" style:type="right" style:leader-style="dotted" style:leader-text="."/></style:tab-stops>
   </style:paragraph-properties>
  </style:style>
 </office:styles>
 <office:automatic-styles>
  <style:page-layout style:name="pm1">
   <style:page-layout-properties fo:page-width="21cm" fo:page-height="29.7cm" fo:margin-top="1.8cm" fo:margin-bottom="1.6cm"
     fo:margin-left="2cm" fo:margin-right="2cm"/>
   <style:footer-style><style:header-footer-properties fo:min-height="0.6cm" fo:margin-top="0.3cm"/></style:footer-style>
  </style:page-layout>
  <style:style style:name="Footer" style:family="paragraph">
   <style:paragraph-properties fo:text-align="center"/>
   <style:text-properties fo:font-size="8pt" style:font-size-asian="8pt" fo:color="#718096"/>
  </style:style>
 </office:automatic-styles>
 <office:master-styles>
  <style:master-page style:name="Standard" style:page-layout-name="pm1">
   <style:footer><text:p text:style-name="Footer">{{footer}} – <text:page-number text:select-page="current"/></text:p></style:footer>
  </style:master-page>
 </office:master-styles>
</office:document-styles>
"""

MANIFEST = """<?xml version="1.0" encoding="UTF-8"?>
<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.3">
 <manifest:file-entry manifest:full-path="/" manifest:media-type="application/vnd.oasis.opendocument.text"/>
 <manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/>
 <manifest:file-entry manifest:full-path="styles.xml" manifest:media-type="text/xml"/>
 <manifest:file-entry manifest:full-path="meta.xml" manifest:media-type="text/xml"/>
</manifest:manifest>
"""


def inline(s):
    """Text mit **fett** in ODF-Spans; Zeilenumbruch \\n wird text:line-break."""
    out = []
    for i, part in enumerate(re.split(r"\*\*(.+?)\*\*", s)):
        part = escape(part).replace("\n", "<text:line-break/>")
        out.append(f'<text:span text:style-name="Strong">{part}</text:span>' if i % 2 else part)
    return "".join(out)


class Document:
    def __init__(self, lang, title, footer):
        self.lang, self.title, self.footer = lang, title, footer
        self.body = []
        self.headings = []  # (Ebene, Text) für das Inhaltsverzeichnis
        self.pages = {}     # Text → Seite (aus einem ersten Rendering, siehe leitfaden.py)
        self.toc_heading = ""
        self.tables = 0
        self.widths = {}

    def title_page(self, title, subtitle, lines):
        self.body.append(f'<text:p text:style-name="Title">{inline(title)}</text:p>')
        self.body.append(f'<text:p text:style-name="Subtitle">{inline(subtitle)}</text:p>')
        for l in lines:
            self.body.append(f'<text:p text:style-name="Body">{inline(l)}</text:p>')

    def toc(self, heading):
        self.toc_heading = heading
        self.body.append(None)  # Platzhalter, gefüllt in save()

    def _toc(self):
        heading = self.toc_heading
        entries = "".join(
            f'<text:p text:style-name="Contents_20_{lvl}">{escape(txt)}<text:tab/>{self.pages.get(txt, "")}</text:p>'
            for lvl, txt in self.headings if lvl <= 2)
        return (
            '<text:table-of-content text:name="Inhalt" text:protected="false">'
            '<text:table-of-content-source text:outline-level="2">'
            f'<text:index-title-template text:style-name="Contents_20_Heading">{escape(heading)}</text:index-title-template>'
            + "".join(
                f'<text:table-of-content-entry-template text:outline-level="{n}" text:style-name="Contents_20_{n}">'
                '<text:index-entry-chapter/><text:index-entry-text/><text:index-entry-tab-stop style:type="right" style:leader-char="."/>'
                '<text:index-entry-page-number/></text:table-of-content-entry-template>' for n in (1, 2))
            + '</text:table-of-content-source><text:index-body>'
            f'<text:index-title text:name="Inhalt_Head"><text:p text:style-name="Contents_20_Heading">{escape(heading)}</text:p></text:index-title>'
            + entries + '</text:index-body></text:table-of-content>')

    def h(self, level, text):
        self.headings.append((level, text))
        self.body.append(f'<text:h text:style-name="Heading_20_{level}" text:outline-level="{level}">{inline(text)}</text:h>')

    def p(self, text, style="Body"):
        self.body.append(f'<text:p text:style-name="{style}">{inline(text)}</text:p>')

    def note(self, text):
        self.p(text, "Note")

    def bullets(self, items):
        self.body.append('<text:list>' + "".join(
            f'<text:list-item><text:p text:style-name="Body">• {inline(i)}</text:p></text:list-item>' for i in items) + '</text:list>')

    def table(self, head, rows, widths=None):
        self.tables += 1
        name = f"T{self.tables}"
        widths = widths or [1] * len(head)
        total = sum(widths)
        self.widths[name] = [17.0 * w / total for w in widths]
        cols = "".join(f'<table:table-column table:style-name="{name}.C{i}"/>' for i in range(len(head)))
        hrow = "".join(f'<table:table-cell table:style-name="CellHead" office:value-type="string">'
                       f'<text:p text:style-name="TableHead">{inline(c)}</text:p></table:table-cell>' for c in head)
        body = ""
        for r in rows:
            body += "<table:table-row>" + "".join(
                f'<table:table-cell table:style-name="Cell" office:value-type="string">'
                + "".join(f'<text:p text:style-name="TableText">{inline(x)}</text:p>' for x in str(c).split("\n\n"))
                + '</table:table-cell>' for c in r) + "</table:table-row>"
        self.body.append(f'<table:table table:name="{name}" table:style-name="{name}">{cols}'
                         f'<table:table-header-rows><table:table-row>{hrow}</table:table-row></table:table-header-rows>{body}</table:table>')
        self.p("")

    def save(self, path):
        auto = ['<style:style style:name="CellHead" style:family="table-cell"><style:table-cell-properties fo:background-color="#1f3a5f" '
                'fo:padding="0.08cm" fo:border="0.5pt solid #1f3a5f"/></style:style>',
                '<style:style style:name="Cell" style:family="table-cell"><style:table-cell-properties fo:padding="0.08cm" '
                'fo:border="0.5pt solid #a0aec0"/></style:style>']
        for name, ws in self.widths.items():
            auto.append(f'<style:style style:name="{name}" style:family="table"><style:table-properties style:width="17cm" '
                        'table:align="left" fo:margin-bottom="0.1cm"/></style:style>')
            for i, w in enumerate(ws):
                auto.append(f'<style:style style:name="{name}.C{i}" style:family="table-column">'
                            f'<style:table-column-properties style:column-width="{w:.2f}cm"/></style:style>')
        lang, country = {"de": ("de", "DE"), "en": ("en", "GB"), "zh-CN": ("zh", "CN")}[self.lang]
        auto.append('<style:style style:name="PLang" style:family="paragraph" style:parent-style-name="Standard">'
                    f'<style:text-properties fo:language="{lang}" fo:country="{country}" style:language-asian="{lang}" style:country-asian="{country}"/></style:style>')
        content = (f'<?xml version="1.0" encoding="UTF-8"?>\n<office:document-content {NS}>'
                   f'<office:automatic-styles>{"".join(auto)}</office:automatic-styles>'
                   f'<office:body><office:text>{"".join(b if b is not None else self._toc() for b in self.body)}'
                   '</office:text></office:body></office:document-content>')
        meta = (f'<?xml version="1.0" encoding="UTF-8"?>\n<office:document-meta {NS}><office:meta>'
                f'<dc:title>{escape(self.title)}</dc:title><dc:language>{self.lang}</dc:language>'
                '<meta:generator>coremesh-erp docs/buchungsleitfaden</meta:generator></office:meta></office:document-meta>')
        styles = STYLES.replace("{footer}", escape(self.footer))
        if self.lang == "zh-CN":
            styles = styles.replace('fo:language="de" fo:country="DE"',
                                    'fo:language="zh" fo:country="CN" style:language-asian="zh" style:country-asian="CN"')
        elif self.lang == "en":
            styles = styles.replace('fo:language="de" fo:country="DE"', 'fo:language="en" fo:country="GB"')
        with zipfile.ZipFile(path, "w") as z:
            z.writestr(zipfile.ZipInfo("mimetype"), "application/vnd.oasis.opendocument.text", compress_type=zipfile.ZIP_STORED)
            for name, data in (("META-INF/manifest.xml", MANIFEST), ("content.xml", content), ("styles.xml", styles), ("meta.xml", meta)):
                z.writestr(name, data, compress_type=zipfile.ZIP_DEFLATED)
