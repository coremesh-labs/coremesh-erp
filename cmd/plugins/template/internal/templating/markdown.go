package templating

import (
	"regexp"
	"strings"
)

// mdtex (seit 0.10.0): ein kleiner Teil von Markdown → LaTeX, für Texte, die
// Anwender pflegen (z. B. Vertragsklauseln). Alles andere wird maskiert, beliebiges
// LaTeX kommt so nicht ins Dokument.
//
//	Leerzeile                 neuer Absatz; einfache Zeilenumbrüche sind Leerzeichen,
//	                          zwei Leerzeichen oder \ am Zeilenende ein Zeilenumbruch
//	**fett**, *kursiv*
//	- Punkt  bzw.  * Punkt    Aufzählung (itemize)
//	1. Punkt                  nummerierte Liste (enumerate)
//	a) Punkt                  Liste a), b), … (enumerate mit Paket enumitem)
//
// Folgezeilen eines Listenpunkts (eingerückt oder ohne neues Zeichen) gehören zu ihm.

var (
	mdBullet = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)
	mdNumber = regexp.MustCompile(`^\s*\d+[.)]\s+(.*)$`)
	mdAlpha  = regexp.MustCompile(`^\s*[a-z]\)\s+(.*)$`)
	mdInline = regexp.MustCompile(`\*\*(.+?)\*\*|\*([^*\s][^*]*?)\*`)
)

func mdtex(v any) string {
	s := strings.ReplaceAll(str(v), "\r\n", "\n")
	var out []string
	for _, block := range strings.Split(strings.TrimSpace(s), "\n\n") {
		if block = strings.Trim(block, "\n"); strings.TrimSpace(block) != "" {
			out = append(out, mdBlock(block))
		}
	}
	return strings.Join(out, "\n\n")
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
