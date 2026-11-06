// Package cm1 schreibt und liest Dateien im Ablageformat cm1 (Spezifikation:
// coremesh-tasks/questions/Ablage_Format_Spezifikation.txt).
//
//	magic "cm1\n" | hlen (4, big-endian) | Kopf (JSON) | blen (8) | Inhalt | [Signatur 64]
//
// Inhalt: erst gepackt (gzip|none), dann mit age für den Schlüssel aus „key“
// verschlüsselt (bei key "" unverschlüsselt). Dateien unter i/ sind mit Ed25519
// über alles vor der Signatur signiert.
package cm1

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"filippo.io/age"
)

// Magic: die ersten vier Bytes jeder Datei.
const Magic = "cm1\n"

// Format: Wert von header.format.
const Format = "cm1"

// Dateiarten.
const (
	KindStart   = "start"
	KindDataset = "dataset"
	KindData    = "data"
	KindModules = "modules"
	KindModule  = "module"
)

// Feste Content-Types der Verwaltungsdateien.
const (
	TypeStart   = "application/vnd.coremesh.start+json;v=1"
	TypeDataset = "application/vnd.coremesh.dataset+json;v=1"
	TypeModules = "application/vnd.coremesh.modules+json;v=1"
	TypeModule  = "application/vnd.coremesh.module+zip;v=1"
	TypeProcess = "application/vnd.coremesh.process+json;v=1"
)

// Packen.
const (
	PackGzip = "gzip"
	PackNone = "none"
)

const maxHeader = 65536

// Header: Kopf einer Datei (Klartext).
type Header struct {
	Format      string `json:"format"`
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Packing     string `json:"packing"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Created     string `json:"created"`
	SignedBy    string `json:"signed_by"`
}

// Fehlercodes beim Lesen (Spezifikation 4).
var (
	ErrHash    = errors.New("E_HASH")
	ErrFormat  = errors.New("E_FORMAT")
	ErrPath    = errors.New("E_PATH")
	ErrSig     = errors.New("E_SIG")
	ErrKey     = errors.New("E_KEY")
	ErrDecrypt = errors.New("E_DECRYPT")
	ErrType    = errors.New("E_TYPE")
	ErrOld     = errors.New("E_OLD")
)

// Signer: Signaturschlüssel des Servers mit Namen ("srv-sig:1").
type Signer struct {
	Name string
	Key  ed25519.PrivateKey
}

// Seal baut eine Datei: Kopf ergänzen (format, size, created, signed_by),
// packen, verschlüsseln (rcpt nil = unverschlüsselt), bei signer signieren.
func Seal(h Header, data []byte, rcpt age.Recipient, signer *Signer, now time.Time) ([]byte, error) {
	h.Format = Format
	h.Size = int64(len(data))
	if h.Created == "" {
		h.Created = now.UTC().Format(time.RFC3339)
	}
	if h.Packing == "" {
		h.Packing = PackNone
	}
	if signer != nil {
		h.SignedBy = signer.Name
	} else {
		h.SignedBy = ""
	}
	if (rcpt == nil) != (h.Key == "") {
		return nil, fmt.Errorf("cm1: Schlüsselname und Schlüssel passen nicht zusammen")
	}
	packed, err := pack(h.Packing, data)
	if err != nil {
		return nil, err
	}
	body := packed
	if rcpt != nil {
		var b bytes.Buffer
		w, err := age.Encrypt(&b, rcpt)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(packed); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		body = b.Bytes()
	}
	hj, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if len(hj) > maxHeader {
		return nil, fmt.Errorf("cm1: Kopf zu groß")
	}
	var out bytes.Buffer
	out.WriteString(Magic)
	binary.Write(&out, binary.BigEndian, uint32(len(hj)))
	out.Write(hj)
	binary.Write(&out, binary.BigEndian, uint64(len(body)))
	out.Write(body)
	if signer != nil {
		out.Write(ed25519.Sign(signer.Key, out.Bytes()))
	}
	return out.Bytes(), nil
}

func pack(p string, data []byte) ([]byte, error) {
	switch p {
	case PackNone:
		return data, nil
	case PackGzip:
		var b bytes.Buffer
		w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		w.Write(data)
		if err := w.Close(); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	return nil, fmt.Errorf("cm1: Packart %q unbekannt", p)
}

// File: zerlegte Datei.
type File struct {
	Header    Header
	Body      []byte
	Signed    []byte // Bytes, über die signiert ist
	Signature []byte
}

// Parse zerlegt eine Datei und prüft den Aufbau (E_FORMAT).
func Parse(b []byte) (*File, error) {
	if len(b) < 4+4 || string(b[:4]) != Magic {
		return nil, fmt.Errorf("%w: kein cm1", ErrFormat)
	}
	hlen := int(binary.BigEndian.Uint32(b[4:8]))
	if hlen > maxHeader || len(b) < 8+hlen+8 {
		return nil, fmt.Errorf("%w: Kopf", ErrFormat)
	}
	var h Header
	dec := json.NewDecoder(bytes.NewReader(b[8 : 8+hlen]))
	if err := dec.Decode(&h); err != nil {
		return nil, fmt.Errorf("%w: Kopf %v", ErrFormat, err)
	}
	p := 8 + hlen
	blen := binary.BigEndian.Uint64(b[p : p+8])
	p += 8
	if blen > uint64(len(b)-p) {
		return nil, fmt.Errorf("%w: Inhalt abgeschnitten", ErrFormat)
	}
	end := p + int(blen)
	f := &File{Header: h, Body: b[p:end], Signed: b[:end]}
	rest := b[end:]
	switch {
	case h.SignedBy != "" && len(rest) == ed25519.SignatureSize:
		f.Signature = rest
	case h.SignedBy == "" && len(rest) == 0:
	default:
		return nil, fmt.Errorf("%w: Bytes nach dem Inhalt", ErrFormat)
	}
	if h.Format != Format || h.Kind == "" || h.ContentType == "" || (h.Packing != PackGzip && h.Packing != PackNone) {
		return nil, fmt.Errorf("%w: Pflichtfelder", ErrFormat)
	}
	return f, nil
}

// Open liest eine Datei vollständig nach Spezifikation 4. path: angefragter
// Pfad (i/…, o/<hex>, m/<hex>); sig: vertraute Signaturschlüssel; ids:
// Schlüssel nach Namen. Ergebnis: Kopf und entpackte Daten.
func Open(path string, b []byte, sig map[string]ed25519.PublicKey, ids map[string]age.Identity) (Header, []byte, error) {
	if strings.HasPrefix(path, "o/") || strings.HasPrefix(path, "m/") {
		if Hash(b) != path[2:] {
			return Header{}, nil, ErrHash
		}
	}
	f, err := Parse(b)
	if err != nil {
		return Header{}, nil, err
	}
	h := f.Header
	if strings.HasPrefix(path, "i/") {
		if h.Path != path {
			return h, nil, ErrPath
		}
		pub, ok := sig[h.SignedBy]
		if !ok || f.Signature == nil || !ed25519.Verify(pub, f.Signed, f.Signature) {
			return h, nil, ErrSig
		}
	}
	packed := f.Body
	if h.Key != "" {
		id, ok := ids[h.Key]
		if !ok {
			return h, nil, ErrKey
		}
		r, err := age.Decrypt(bytes.NewReader(f.Body), id)
		if err != nil {
			return h, nil, fmt.Errorf("%w: %v", ErrDecrypt, err)
		}
		if packed, err = io.ReadAll(r); err != nil {
			return h, nil, fmt.Errorf("%w: %v", ErrDecrypt, err)
		}
	}
	data := packed
	if h.Packing == PackGzip {
		r, err := gzip.NewReader(bytes.NewReader(packed))
		if err != nil {
			return h, nil, fmt.Errorf("%w: gzip", ErrFormat)
		}
		if data, err = io.ReadAll(r); err != nil {
			return h, nil, fmt.Errorf("%w: gzip", ErrFormat)
		}
	}
	if int64(len(data)) != h.Size {
		return h, nil, fmt.Errorf("%w: Größe", ErrFormat)
	}
	return h, data, nil
}

// Hash: SHA-256 der ganzen Datei, hex klein (Name unter o/ und m/).
func Hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewID: 16 Zufallsbytes, base32 klein ohne „=“ (26 Zeichen).
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(b32.EncodeToString(b))
}

// B64 kodiert base64url ohne „=“ (öffentliche Signaturschlüssel, Signaturen, Link).
func B64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// UnB64 dekodiert base64url ohne „=“.
func UnB64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// NewAgeKey: neues age-Schlüsselpaar (geheim "AGE-SECRET-KEY-1…", öffentlich "age1…").
func NewAgeKey() (secret, public string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return id.String(), id.Recipient().String(), nil
}

// Recipient: öffentlicher age-Schlüssel.
func Recipient(public string) (age.Recipient, error) { return age.ParseX25519Recipient(public) }

// Identity: geheimer age-Schlüssel.
func Identity(secret string) (age.Identity, error) { return age.ParseX25519Identity(secret) }

// SigText: Text, über den ein neuer Signaturschlüssel mit dem bisherigen signiert wird.
func SigText(key, public, validFrom string) []byte {
	return []byte("cm1-srv-sig\n" + key + "\n" + public + "\n" + validFrom)
}

// SetupLink: Einrichtungslink https://<pwa>/#cm1:<base64url(JSON)>.
type SetupLink struct {
	V      int        `json:"v"`
	Ablage string     `json:"ablage"`
	Start  string     `json:"start"`
	Name   string     `json:"name"`
	Key    LinkKey    `json:"key"`
	Sig    LinkSigner `json:"sig"`
}

type LinkKey struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

type LinkSigner struct {
	Key    string `json:"key"`
	Public string `json:"public"`
}

// URL: Link zur PWA mit den Daten hinter #.
func (l SetupLink) URL(pwa string) string {
	l.V = 1
	b, _ := json.Marshal(l)
	return strings.TrimRight(pwa, "#") + "#cm1:" + B64(b)
}

// ParseSetupLink liest die Daten aus einem Einrichtungslink.
func ParseSetupLink(u string) (SetupLink, error) {
	_, frag, ok := strings.Cut(u, "#cm1:")
	if !ok {
		return SetupLink{}, fmt.Errorf("cm1: kein Einrichtungslink")
	}
	b, err := UnB64(frag)
	if err != nil {
		return SetupLink{}, err
	}
	var l SetupLink
	err = json.Unmarshal(b, &l)
	return l, err
}
