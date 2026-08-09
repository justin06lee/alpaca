package netx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// certLifetime is deliberately long. The certificate is pinned by fingerprint
// in connect strings the user has already pasted onto other machines, so an
// expiry would silently break working setups with no upgrade path. Rotation is
// an explicit action (`alpaca serve --rotate-cert`), not a timer.
const certLifetime = 10 * 365 * 24 * time.Hour

// Identity is the server's TLS material plus the fingerprint clients pin.
type Identity struct {
	Certificate tls.Certificate
	// Fingerprint is the lowercase hex SHA-256 of the leaf certificate's DER
	// encoding — the same value `openssl x509 -fingerprint -sha256` prints.
	Fingerprint string
}

// Fingerprint hashes a DER-encoded certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// LoadOrCreateIdentity returns the persisted certificate for this server,
// generating one on first run.
//
// The certificate is reused across restarts because its fingerprint is baked
// into every connect string already in circulation; regenerating would revoke
// every client at once.
func LoadOrCreateIdentity(dir string, hosts []string) (*Identity, error) {
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")

	if id, err := loadIdentity(certPath, keyPath); err == nil {
		return id, nil
	} else if !os.IsNotExist(err) {
		// A corrupt or unreadable pair is worth reporting rather than silently
		// replacing: silently replacing it would invalidate existing clients.
		return nil, fmt.Errorf("load tls identity (delete %s and %s to regenerate): %w",
			certPath, keyPath, err)
	}

	return CreateIdentity(dir, hosts)
}

// CreateIdentity mints a fresh self-signed certificate and persists it,
// replacing any existing one.
func CreateIdentity(dir string, hosts []string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create tls dir: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "alpaca", Organization: []string{"alpaca self-hosted"}},
		// Backdate slightly so a client whose clock runs behind the server's
		// does not reject a certificate minted seconds ago.
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(certLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	// SANs are cosmetic for alpaca's own client, which pins the fingerprint and
	// skips name checks. They matter for everything else pointed at the
	// gateway — curl, browsers, other OpenAI-compatible tooling.
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else if host != "" {
			template.DNSNames = append(template.DNSNames, host)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write certificate: %w", err)
	}
	// The private key is the one file here that must never be world-readable.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("assemble keypair: %w", err)
	}
	return &Identity{Certificate: cert, Fingerprint: Fingerprint(der)}, nil
}

func loadIdentity(certPath, keyPath string) (*Identity, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse keypair: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("certificate file contains no certificate")
	}
	return &Identity{Certificate: cert, Fingerprint: Fingerprint(cert.Certificate[0])}, nil
}

// ServerTLSConfig builds the server side of the TLS fallback path.
func (id *Identity) ServerTLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.Certificate},
		MinVersion:   tls.VersionTLS12,
	}
}

// PinnedClientConfig verifies the server by exact certificate fingerprint
// instead of by certificate authority.
//
// InsecureSkipVerify looks alarming here and is doing the opposite of what the
// name suggests. It disables Go's *default* checks — chain-to-a-public-CA and
// hostname matching — neither of which can succeed for a self-signed cert
// reached at whatever IP the router handed out today. VerifyPeerCertificate
// then applies a strictly stronger check: the certificate must be byte-for-byte
// the one whose hash the user carried over in the connect string. A public CA
// mis-issuing for this host, or an attacker on the path, both fail this.
func PinnedClientConfig(fingerprint string) *tls.Config {
	want := []byte(fingerprint)
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // replaced by the pin below
		MinVersion:         tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("server presented no certificate")
			}
			got := []byte(Fingerprint(rawCerts[0]))
			if subtle.ConstantTimeCompare(got, want) != 1 {
				return fmt.Errorf("certificate fingerprint mismatch:\n  expected %s\n  received %s\n"+
					"the server's certificate changed, or something is intercepting the connection",
					fingerprint, got)
			}
			return nil
		},
	}
}
