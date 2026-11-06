package export

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/cm1"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"golang.org/x/crypto/chacha20poly1305"
)

// Schlüsselmanager (Konzept Ablage_Dateiformat_Konzept.txt 4, 7.7, 7.8)
//
//   - Hauptschlüssel: Datei außerhalb der Datenbank (Einstellung key_file, Standard
//     data/keys/export-master.key), beim ersten Gebrauch erzeugt. Mit ihm sind alle
//     geheimen Schlüssel in export__key verschlüsselt (XChaCha20-Poly1305).
//   - Serverschlüssel: srv-sig:<n> (Ed25519, signiert jede i/-Datei), srv-box:<n>
//     (age, Rückkanal). Einmal je Installation (company_code leer).
//   - Dataset-Schlüssel ds:<ablage-id>:<n> (age) mit Fassungen; neue Fassung, wenn
//     jemand die Berechtigung verliert.
//   - Benutzerschlüssel user:<uid>:<n>: nur der öffentliche Teil – der private steht
//     einmal im Einrichtungslink und wird nicht gespeichert.

const (
	keySrvSig  = "SRV_SIG"
	keySrvBox  = "SRV_BOX"
	keyDataset = "DATASET"
	keyUser    = "USER"
	keyMaster  = "MASTER" // nur Vermerk „gesichert am“, kein Schlüssel

	defaultKeyFile = "data/keys/export-master.key"
)

// master: Hauptschlüssel laden bzw. anlegen.
func (m *Module) master(ctx context.Context) ([]byte, error) {
	m.keyMu.Lock()
	defer m.keyMu.Unlock()
	if m.masterKey != nil {
		return m.masterKey, nil
	}
	path := firstNonEmpty(m.cfg.KeyFile, defaultKeyFile)
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) != chacha20poly1305.KeySize {
			return nil, fmt.Errorf("Hauptschlüssel %s unlesbar", path)
		}
		m.masterKey = k
		return k, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	// Ohne Hauptschlüssel, aber mit verschlüsselten Schlüsseln in der Datenbank: nicht
	// still einen neuen anlegen (die alten wären unlesbar).
	if m.secretsExist(ctx) {
		return nil, fmt.Errorf("Hauptschlüssel %s fehlt, in der Datenbank liegen aber verschlüsselte Schlüssel – Sicherung einspielen", path)
	}
	k := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(k)+"\n"), 0o600); err != nil {
		return nil, err
	}
	m.masterKey = k
	return k, nil
}

// seal: Geheimnis mit dem Hauptschlüssel verschlüsseln ("x1:" + base64(nonce|ct)).
func (m *Module) seal(ctx context.Context, plain string) (string, error) {
	k, err := m.master(ctx)
	if err != nil {
		return "", err
	}
	a, err := chacha20poly1305.NewX(k)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return "x1:" + base64.StdEncoding.EncodeToString(a.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (m *Module) unseal(ctx context.Context, s string) (string, error) {
	b64, ok := strings.CutPrefix(s, "x1:")
	if !ok {
		return "", fmt.Errorf("Schlüssel unbekannt verschlüsselt")
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	k, err := m.master(ctx)
	if err != nil {
		return "", err
	}
	a, err := chacha20poly1305.NewX(k)
	if err != nil {
		return "", err
	}
	if len(b) < a.NonceSize() {
		return "", fmt.Errorf("Schlüssel beschädigt")
	}
	p, err := a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("Schlüssel passt nicht zum Hauptschlüssel")
	}
	return string(p), nil
}

func (m *Module) secretsExist(ctx context.Context) bool {
	r, err := m.one(ctx, "SELECT COUNT(*) FROM export__key WHERE secret IS NOT NULL")
	return err == nil && r != nil && toInt(r[0]) > 0
}

// serverKeys: Signatur- und Rückkanalschlüssel des Servers (beim ersten Gebrauch erzeugt).
type serverKeys struct {
	signer  *cm1.Signer
	sigs    []sigKey
	boxName string
	boxPub  string
}

type sigKey struct{ name, public, since string }

func (m *Module) serverKeys(ctx context.Context) (*serverKeys, error) {
	if err := m.ensureServerKeys(ctx); err != nil {
		return nil, err
	}
	res, err := m.db.Query(ctx, `SELECT key_name, kind, COALESCE(public, ''), COALESCE(secret, ''), created_at FROM export__key
		WHERE kind IN (?, ?) AND revoked_at IS NULL ORDER BY kind, version`, keySrvSig, keySrvBox)
	if err != nil {
		return nil, err
	}
	sk := &serverKeys{}
	for _, r := range res.Rows {
		name, kind, pub, sec, at := crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[3]), crud.Str(r[4])
		switch kind {
		case keySrvSig:
			sk.sigs = append(sk.sigs, sigKey{name, pub, dateOf(at)})
			seed, err := m.unseal(ctx, sec)
			if err != nil {
				return nil, err
			}
			b, err := cm1.UnB64(seed)
			if err != nil || len(b) != ed25519.SeedSize {
				return nil, fmt.Errorf("Signaturschlüssel %s beschädigt", name)
			}
			sk.signer = &cm1.Signer{Name: name, Key: ed25519.NewKeyFromSeed(b)} // der neueste signiert
		case keySrvBox:
			sk.boxName, sk.boxPub = name, pub
		}
	}
	return sk, nil
}

func dateOf(stamp string) string {
	if len(stamp) >= 10 {
		return stamp[:10]
	}
	return stamp
}

func (m *Module) ensureServerKeys(ctx context.Context) error {
	r, err := m.one(ctx, "SELECT COUNT(*) FROM export__key WHERE kind = ? AND revoked_at IS NULL", keySrvSig)
	if err != nil {
		return err
	}
	if toInt(r[0]) == 0 {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return err
		}
		sec, err := m.seal(ctx, cm1.B64(seed))
		if err != nil {
			return err
		}
		pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		if err := m.insertKey(ctx, "srv-sig:1", keySrvSig, 1, "", "", "", cm1.B64(pub), sec); err != nil {
			return err
		}
	}
	r, err = m.one(ctx, "SELECT COUNT(*) FROM export__key WHERE kind = ? AND revoked_at IS NULL", keySrvBox)
	if err != nil {
		return err
	}
	if toInt(r[0]) == 0 {
		s, p, err := cm1.NewAgeKey()
		if err != nil {
			return err
		}
		sec, err := m.seal(ctx, s)
		if err != nil {
			return err
		}
		return m.insertKey(ctx, "srv-box:1", keySrvBox, 1, "", "", "", p, sec)
	}
	return nil
}

func (m *Module) insertKey(ctx context.Context, name, kind string, version int, cc, dataset, participant, public, secret string) error {
	_, err := m.db.Exec(ctx, `INSERT INTO export__key (key_name, kind, created_at, company_code, dataset_id, participant_id, public, secret, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, name, kind, m.stamp(), nilIfEmpty(cc), nilIfEmpty(dataset), nilIfEmpty(participant), nilIfEmpty(public), nilIfEmpty(secret), version)
	return err
}

// dsKey: Fassung eines Dataset-Schlüssels.
type dsKeyVer struct {
	name, public, secret string
	version              int
	since                string
}

// datasetKeys: alle Fassungen (aufsteigend); leer, wenn es noch keine gibt.
func (m *Module) datasetKeys(ctx context.Context, cc, dsID string, withSecret bool) ([]dsKeyVer, error) {
	res, err := m.db.Query(ctx, `SELECT key_name, COALESCE(public, ''), COALESCE(secret, ''), COALESCE(version, 0), created_at FROM export__key
		WHERE kind = ? AND company_code = ? AND dataset_id = ? ORDER BY version`, keyDataset, cc, dsID)
	if err != nil {
		return nil, err
	}
	var out []dsKeyVer
	for _, r := range res.Rows {
		k := dsKeyVer{name: crud.Str(r[0]), public: crud.Str(r[1]), version: toInt(r[3]), since: dateOf(crud.Str(r[4]))}
		if withSecret {
			if k.secret, err = m.unseal(ctx, crud.Str(r[2])); err != nil {
				return nil, err
			}
		}
		out = append(out, k)
	}
	return out, nil
}

// currentDatasetKey: neueste Fassung, beim ersten Mal Fassung 1.
func (m *Module) currentDatasetKey(ctx context.Context, cc, dsID, storeID string) (dsKeyVer, error) {
	ks, err := m.datasetKeys(ctx, cc, dsID, false)
	if err != nil {
		return dsKeyVer{}, err
	}
	if len(ks) > 0 {
		return ks[len(ks)-1], nil
	}
	return m.newDatasetKey(ctx, cc, dsID, storeID, 1)
}

func (m *Module) newDatasetKey(ctx context.Context, cc, dsID, storeID string, version int) (dsKeyVer, error) {
	s, p, err := cm1.NewAgeKey()
	if err != nil {
		return dsKeyVer{}, err
	}
	sec, err := m.seal(ctx, s)
	if err != nil {
		return dsKeyVer{}, err
	}
	name := fmt.Sprintf("ds:%s:%d", storeID, version)
	if err := m.insertKey(ctx, name, keyDataset, version, cc, dsID, "", p, sec); err != nil {
		return dsKeyVer{}, err
	}
	return dsKeyVer{name: name, public: p, version: version, since: m.today()}, nil
}

// rotateDatasetKey: neue Fassung (Berechtigung entzogen).
func (m *Module) rotateDatasetKey(ctx context.Context, cc, dsID, storeID string) (dsKeyVer, error) {
	ks, err := m.datasetKeys(ctx, cc, dsID, false)
	if err != nil {
		return dsKeyVer{}, err
	}
	return m.newDatasetKey(ctx, cc, dsID, storeID, len(ks)+1)
}
