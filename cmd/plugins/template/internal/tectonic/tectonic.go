// Package tectonic bettet Tectonic (LaTeX → PDF, XeTeX) für die Plattform ein,
// für die das Plugin gebaut wird. tools/fetch-tectonic.sh lädt es vor dem Bauen
// nach bin/<os>-<arch>/tectonic[.exe].gz; fehlt die Datei, liefert Binary nil
// und das Plugin nimmt tectonic_path bzw. tectonic im Suchpfad.
package tectonic

import "runtime"

// Binary: eingebettetes Tectonic (gzip) und Dateiname des Programms; nil, wenn nicht eingebettet.
func Binary() (gz []byte, name string) {
	name = "tectonic"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if platform == "" {
		return nil, name
	}
	b, err := files.ReadFile("bin/" + platform + "/" + name + ".gz")
	if err != nil {
		return nil, name
	}
	return b, name
}

// Version: Version des eingebetteten Tectonic ("" = keins).
func Version() string {
	if platform == "" {
		return ""
	}
	b, err := files.ReadFile("bin/" + platform + "/tectonic.gz.version")
	if err != nil {
		b, err = files.ReadFile("bin/" + platform + "/tectonic.exe.gz.version")
	}
	if err != nil {
		return ""
	}
	return string(trimNL(b))
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
