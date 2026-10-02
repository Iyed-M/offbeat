package lansync

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"syscall"
	"time"
)

const identityFilename = "lan-sync.json"

// Identity is owned by the daemon and never belongs in status or logs.
type Identity struct {
	CertificatePEM []byte `json:"certificate_pem"`
	PrivateKeyPEM  []byte `json:"private_key_pem"`
	Credential     string `json:"credential"`
}

func NewCredential() string {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		panic("secure randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(secret[:])
}

func LoadIdentity(directory string) (*Identity, error) {
	root, err := privateRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.OpenFile(identityFilename, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		identity, err := newIdentity()
		if err != nil {
			return nil, err
		}
		if err := identity.Save(directory); err != nil {
			return nil, err
		}
		return identity, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot open private LAN identity")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		return nil, fmt.Errorf("LAN identity must be a bounded private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil {
		return nil, fmt.Errorf("cannot read LAN identity")
	}
	var identity Identity
	if json.Unmarshal(data, &identity) != nil {
		return nil, fmt.Errorf("invalid LAN identity")
	}
	if _, err := identity.TLSCertificate(); err != nil {
		return nil, err
	}
	credential, err := base64.RawURLEncoding.DecodeString(identity.Credential)
	if err != nil || len(credential) != 32 {
		return nil, fmt.Errorf("invalid phone credential")
	}
	return &identity, nil
}

func privateRoot(directory string) (*os.Root, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("LAN identity directory must be private (0700)")
	}
	return os.OpenRoot(directory)
}

func (i *Identity) Save(directory string) error {
	root, err := privateRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.Marshal(i)
	if err != nil {
		return err
	}
	name := ".lan-sync-" + rand.Text()
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot stage LAN identity")
	}
	defer root.Remove(name)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("cannot write LAN identity")
	}
	if err := root.Rename(name, identityFilename); err != nil {
		return fmt.Errorf("cannot publish LAN identity")
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func newIdentity() (*Identity, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Offbeat desktop"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		return nil, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	return &Identity{CertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), Credential: NewCredential()}, nil
}

func (i *Identity) TLSCertificate() (tls.Certificate, error) {
	certificate, err := tls.X509KeyPair(i.CertificatePEM, i.PrivateKeyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("invalid desktop TLS identity")
	}
	return certificate, nil
}

func (i *Identity) Fingerprint() string {
	block, _ := pem.Decode(i.CertificatePEM)
	if block == nil {
		return ""
	}
	return ContentVersion(block.Bytes)
}
