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
	"strings"
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

// NormalizeFingerprint reduces a SHA-256 fingerprint to the canonical form
// Fingerprint produces: lowercase hex, no separators. `openssl x509
// -fingerprint` prints uppercase pairs joined by colons, and a user pasting
// that form should get a working pin, not a mismatch that reads like an attack.
func NormalizeFingerprint(fp string) string {
	fp = strings.Map(func(r rune) rune {
		if r == ':' || r == ' ' {
			return -1
		}
		return r
	}, fp)
	return strings.ToLower(fp)
}

// MissingSANs lists which of hosts the certificate does not cover.
//
// The persisted certificate keeps whatever SANs it was minted with, while the
// machine's addresses drift underneath it. alpaca's own client pins the
// fingerprint and never looks at names, but curl and browsers do; serve uses
// this to say so at startup instead of regenerating — which would break every
// pinned client — or staying silent.
func (id *Identity) MissingSANs(hosts []string) []string {
	if len(id.Certificate.Certificate) == 0 {
		return nil
	}
	leaf, err := x509.ParseCertificate(id.Certificate.Certificate[0])
	if err != nil {
		return nil
	}
	var missing []string
	for _, host := range hosts {
		// VerifyHostname accepts both DNS names and IP literals.
		if host != "" && leaf.VerifyHostname(host) != nil {
			missing = append(missing, host)
		}
	}
	return missing
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

	// Mint a fresh identity only when both halves are affirmatively absent — a
	// first run. Anything short of that (one file gone, a permission error, a
	// corrupt pair) is reported rather than papered over, because replacing the
	// certificate silently would invalidate every connect string in circulation.
	if fileMissing(certPath) && fileMissing(keyPath) {
		return CreateIdentity(dir, hosts)
	}
	if fileMissing(keyPath) {
		return nil, fmt.Errorf("tls certificate %s exists but its private key %s is missing; "+
			"restore the key from backup, or run `alpaca serve --rotate-cert` to mint a new identity "+
			"(every linked client must then re-link)", certPath, keyPath)
	}
	if fileMissing(certPath) {
		return nil, fmt.Errorf("tls private key %s exists but its certificate %s is missing; "+
			"restore the certificate from backup, or run `alpaca serve --rotate-cert` to mint a new identity "+
			"(every linked client must then re-link)", keyPath, certPath)
	}

	id, err := loadIdentity(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load tls identity (delete %s and %s to regenerate): %w",
			certPath, keyPath, err)
	}
	return id, nil
}

// fileMissing is deliberately narrower than "cannot be read": a permission
// error must not be mistaken for a first run.
func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
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
	// Normalized once here so a colon-separated or uppercase pin — the format
	// other tools print — still matches the canonical form we hash to.
	want := []byte(NormalizeFingerprint(fingerprint))
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
					want, got)
			}
			return nil
		},
	}
}
