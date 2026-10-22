## Eingebettetes Programm: Tectonic

Das Plugin `template` enthält das Programm **Tectonic** (LaTeX → PDF, Version laut
`tools/fetch-tectonic.sh`, derzeit 0.17.0) unverändert als eigenständige ausführbare Datei; es wird
zur Laufzeit als eigener Prozess gestartet (Aggregation, kein Linken).

- Tectonic steht unter der MIT-Lizenz (Text unten). Laut Tectonic stammen Teile aus Projekten
  mit vielen anderen freien Lizenzen; statisch enthalten sind u. a. XeTeX/xdvipdfmx (teils
  GPL-2.0-or-later), HarfBuzz (MIT), ICU (Unicode-3.0), FreeType (FTL oder GPL-2.0),
  Graphite2 (LGPL-2.1, MPL oder GPL), libpng (libpng-Lizenz), zlib (Zlib), Fontconfig
  (MIT-ähnlich) und Rust-Crates (MIT/Apache-2.0).
- Den vollständigen Quelltext dieser Fassung gibt es unter
  https://github.com/tectonic-typesetting/tectonic/releases/tag/tectonic%400.17.0
  (Angebot nach GPL: auf Anfrage stellen wir ihn auch selbst bereit).
- TeX-Pakete und Schriften lädt Tectonic bei Bedarf aus seinem Bundle; sie werden nicht mit
  CoreMesh ausgeliefert und stehen unter ihren eigenen Lizenzen (überwiegend LPPL, OFL, GPL).

```
(Tectonic is licensed under the MIT License. Elements of the system from which
it is derived are licensed under an extremely wide variety of open-source
licenses.)

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the “Software”), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
