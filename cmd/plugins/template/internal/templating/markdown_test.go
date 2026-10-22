package templating

import "testing"

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
	} {
		if got := mdtex(c.in); got != c.want {
			t.Errorf("%q:\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}
