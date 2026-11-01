package templating

import (
	"regexp"
	"strings"
)

// mdtex (seit 0.10.0): ein kleiner Teil von Markdown → LaTeX, für Texte, die
// Anwender pflegen (z. B. Vertragsklauseln). Alles andere wird maskiert, beliebiges
// LaTeX kommt so nicht ins Dokument.
//
//	# Titel, ## Abschnitt     Überschriften (seit 0.13.0; ###, #### kleiner), fett ohne Nummer
//	Leerzeile                 neuer Absatz; einfache Zeilenumbrüche sind Leerzeichen,
//	                          zwei Leerzeichen oder \ am Zeilenende ein Zeilenumbruch
//	**fett**, *kursiv*
//	- Punkt  bzw.  * Punkt    Aufzählung (itemize)
//	1. Punkt                  nummerierte Liste (enumerate)
//	a) Punkt                  Liste a), b), … (enumerate mit Paket enumitem)
//	| Grundmiete | EUR 850,00 |  Tabelle (seit 0.11.0): je Zeile eine Zeile der Tabelle
//	|---|--:|                 Linie; :-- links, --: rechts, :-: mittig (ohne Angabe die
//	                          erste Spalte links, die übrigen rechts)
//
// Folgezeilen eines Listenpunkts (eingerückt oder ohne neues Zeichen) gehören zu ihm.
// Tabellenzeilen, die nur durch Leerzeilen getrennt sind, bilden eine Tabelle – so
// setzen mehrere Vertragsklauseln eines Absatzes eine Tabelle zusammen.

var (
	mdBullet = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)
	mdNumber = regexp.MustCompile(`^\s*\d+[.)]\s+(.*)$`)
	mdAlpha  = regexp.MustCompile(`^\s*[a-z]\)\s+(.*)$`)
	mdInline = regexp.MustCompile(`\*\*(.+?)\*\*|\*([^*\s][^*]*?)\*`)
	mdRule   = regexp.MustCompile(`^:?-+:?$`)
	mdHead   = regexp.MustCompile(`^\s*(#{1,4})\s+(.*?)\s*#*\s*$`)
)

// mdHeading: Überschrift als fetter Absatz ohne Nummer (in Briefen und Berichten gleich).
func mdHeading(level int, text string) string {
	t := mdInlineTex(text)
	switch level {
	case 1:
		return `\par\medskip\noindent{\large\bfseries ` + t + `}\par\nopagebreak\smallskip`
	case 2:
		return `\par\smallskip\noindent{\bfseries ` + t + `}\par\nopagebreak`
	}
	return `\par\noindent{\bfseries\itshape ` + t + `}\par\nopagebreak`
}

func mdtex(v any) string {
	s := strings.ReplaceAll(str(v), "\r\n", "\n")
	var out []string
	var table []string
	flush := func() {
		if len(table) > 0 {
			out = append(out, mdTable(table))
			table = nil
		}
	}
	for _, block := range strings.Split(strings.TrimSpace(s), "\n\n") {
		if block = strings.Trim(block, "\n"); strings.TrimSpace(block) == "" {
			continue
		}
		// Tabellenzeilen sammeln (auch über Leerzeilen hinweg), Text dazwischen als Absatz.
		var text []string
		for _, l := range strings.Split(block, "\n") {
			if h := mdHead.FindStringSubmatch(l); h != nil {
				flush()
				if len(text) > 0 {
					out = append(out, mdBlock(strings.Join(text, "\n")))
					text = nil
				}
				out = append(out, mdHeading(len(h[1]), h[2]))
				continue
			}
			if isTableLine(l) {
				if len(text) > 0 {
					flush()
					out = append(out, mdBlock(strings.Join(text, "\n")))
					text = nil
				}
				table = append(table, strings.TrimSpace(l))
				continue
			}
			text = append(text, l)
		}
		if len(text) > 0 {
			flush()
			out = append(out, mdBlock(strings.Join(text, "\n")))
		}
	}
	flush()
	return strings.Join(out, "\n\n")
}

func isTableLine(l string) bool {
	l = strings.TrimSpace(l)
	return len(l) > 1 && strings.HasPrefix(l, "|")
}

// tableCells: Zellen einer Zeile „| a | b |“.
func tableCells(l string) []string {
	l = strings.TrimSuffix(strings.TrimPrefix(l, "|"), "|")
	cells := strings.Split(l, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

// isRuleRow: Zeile aus Strichen („|---|--:|“) – Linie und Ausrichtung.
func isRuleRow(cells []string) bool {
	for _, c := range cells {
		if !mdRule.MatchString(c) {
			return false
		}
	}
	return true
}

// mdTable: Tabellenzeilen → tabular (Breite nach Inhalt).
func mdTable(lines []string) string {
	cols := 0
	rows := make([][]string, len(lines))
	for i, l := range lines {
		rows[i] = tableCells(l)
		cols = max(cols, len(rows[i]))
	}
	align := make([]string, cols)
	for i := range align {
		align[i] = "r"
	}
	align[0] = "l"
	for _, r := range rows {
		if !isRuleRow(r) {
			continue
		}
		for i, c := range r {
			switch {
			case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":"):
				align[i] = "c"
			case strings.HasSuffix(c, ":"):
				align[i] = "r"
			case strings.HasPrefix(c, ":"):
				align[i] = "l"
			}
		}
	}
	parts := []string{`\par\noindent\begin{tabular}{@{}` + strings.Join(align, `@{\quad}`) + `@{}}`}
	for _, r := range rows {
		if isRuleRow(r) {
			parts = append(parts, `\hline`)
			continue
		}
		cells := make([]string, cols)
		for i := range cells {
			if i < len(r) {
				cells[i] = mdInlineTex(r[i])
			}
		}
		parts = append(parts, strings.Join(cells, " & ")+` \\`)
	}
	return strings.Join(append(parts, `\end{tabular}\par`), "\n")
}

// mdBlock: ein Absatz oder eine Liste (die Art bestimmt die erste Zeile).
func mdBlock(block string) string {
	lines := strings.Split(block, "\n")
	kinds := []struct {
		re         *regexp.Regexp
		begin, end string
	}{
		{mdBullet, `\begin{itemize}`, `\end{itemize}`},
		{mdAlpha, `\begin{enumerate}[label=\alph*)]`, `\end{enumerate}`},
		{mdNumber, `\begin{enumerate}`, `\end{enumerate}`},
	}
	for _, k := range kinds {
		if !k.re.MatchString(lines[0]) {
			continue
		}
		var items [][]string
		for _, l := range lines {
			if m := k.re.FindStringSubmatch(l); m != nil {
				items = append(items, []string{m[1]})
			} else {
				items[len(items)-1] = append(items[len(items)-1], l)
			}
		}
		parts := []string{k.begin}
		for _, it := range items {
			parts = append(parts, `\item `+mdLines(it))
		}
		return strings.Join(append(parts, k.end), "\n")
	}
	return mdLines(lines)
}

// mdLines: Zeilen eines Absatzes; harter Umbruch bei zwei Leerzeichen oder \ am Ende.
func mdLines(lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		hard := strings.HasSuffix(l, "  ") || strings.HasSuffix(strings.TrimRight(l, " "), `\`)
		l = strings.TrimSuffix(strings.TrimSpace(l), `\`)
		b.WriteString(mdInlineTex(strings.TrimSpace(l)))
		if i < len(lines)-1 {
			if hard {
				b.WriteString("\\\\\n")
			} else {
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// mdInlineTex: fett und kursiv, alles andere maskiert (Unterstriche bleiben Text).
func mdInlineTex(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range mdInline.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(texEscape(s[last:m[0]]))
		switch {
		case m[2] >= 0:
			b.WriteString(`\textbf{` + mdInlineTex(s[m[2]:m[3]]) + `}`)
		default:
			b.WriteString(`\emph{` + texEscape(s[m[4]:m[5]]) + `}`)
		}
		last = m[1]
	}
	b.WriteString(texEscape(s[last:]))
	return b.String()
}
