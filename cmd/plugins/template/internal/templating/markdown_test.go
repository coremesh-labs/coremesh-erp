package templating

import (
	"strings"
	"testing"
)

func TestMdtex(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Die Miete beträgt **850,00 €** und ist *im Voraus* fällig.", `Die Miete beträgt \textbf{850,00 €} und ist \emph{im Voraus} fällig.`},
		{"Zeile eins\nZeile zwei\n\nNeuer Absatz", "Zeile eins\nZeile zwei\n\nNeuer Absatz"},
		{"Kantstr. 17  \n04275 Leipzig", "Kantstr. 17\\\\\n04275 Leipzig"},
		{"100 % & $ {x} STEUER_ID \\cmd", `100 \% \& \$ \{x\} STEUER\_ID \textbackslash{}cmd`},
		{"a) in Küchen alle 5 Jahre\nb) in Wohnräumen\n   alle 7 Jahre", "\\begin{enumerate}[label=\\alph*)]\n\\item in Küchen alle 5 Jahre\n\\item in Wohnräumen\nalle 7 Jahre\n\\end{enumerate}"},
		{"- 3 Zimmer\n- 1 Küche", "\\begin{itemize}\n\\item 3 Zimmer\n\\item 1 Küche\n\\end{itemize}"},
		{"1. erstens\n2. zweitens", "\\begin{enumerate}\n\\item erstens\n\\item zweitens\n\\end{enumerate}"},
		{"5 * 3 = 15", `5 * 3 = 15`},
		{"Die Miete beträgt:\n| Grundmiete | EUR 850,00 |\n\n| Heizkosten | EUR 90,00 |\n\n|---|---|\n| **Gesamt** | **EUR 940,00** |\n\nFällig am 3. Werktag.",
			"Die Miete beträgt:\n\n\\par\\noindent\\begin{tabular}{@{}l@{\\quad}r@{}}\nGrundmiete & EUR 850,00 \\\\\nHeizkosten & EUR 90,00 \\\\\n\\hline\n\\textbf{Gesamt} & \\textbf{EUR 940,00} \\\\\n\\end{tabular}\\par\n\nFällig am 3. Werktag."},
		{"| a | b | c |\n|:-:|:--|---|\n| 1 | 2 |", "\\par\\noindent\\begin{tabular}{@{}c@{\\quad}l@{\\quad}r@{}}\na & b & c \\\\\n\\hline\n1 & 2 &  \\\\\n\\end{tabular}\\par"},
	} {
		if got := mdtex(c.in); got != c.want {
			t.Errorf("%q:\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

// TestMdtexHeadings (0.13.0): Überschriften als fette Absätze, auch direkt vor Text und Listen.
func TestMdtexHeadings(t *testing.T) {
	got := mdtex("# Ergebnis\nDie Abrechnung ist **geprüft**.\n\n## Offene Punkte\n- Heizung\n\n### Hinweis #")
	for _, want := range []string{`{\large\bfseries Ergebnis}`, `\textbf{geprüft}`, `{\bfseries Offene Punkte}`, `\begin{itemize}`, `{\bfseries\itshape Hinweis}`} {
		if !strings.Contains(got, want) {
			t.Errorf("fehlt %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "#") {
		t.Errorf("Raute übrig:\n%s", got)
	}
}
