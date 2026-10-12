package lab

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// Certs holds the lab CA and one leaf per TLS failure mode.
type Certs struct {
	CA      *x509.Certificate
	CAKey   *ecdsa.PrivateKey
	CAPEM   []byte
	Pool    *x509.CertPool
	Valid   tls.Certificate // *.lab.test, 127.0.0.1
	Expired tls.Certificate
	NotYet  tls.Certificate
	Wrong   tls.Certificate // covers only wrong.example.net
	SelfSig tls.Certificate // self-signed *.lab.test
	Soon    tls.Certificate // expires in 5 days
}

// NewCerts generates a fresh CA and leaves relative to now.
func NewCerts(now time.Time) (*Certs, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca key: %w", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Sebab Failure Lab CA"},
		NotBefore:             now.Add(-365 * 24 * time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("ca cert: %w", err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	c := &Certs{CA: ca, CAKey: caKey, Pool: x509.NewCertPool(),
		CAPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})}
	c.Pool.AddCert(ca)

	labNames := []string{"*.lab.test", "lab.test", "localhost"}
	day := 24 * time.Hour
	leaves := []struct {
		dst        *tls.Certificate
		names      []string
		from, to   time.Time
		selfSigned bool
	}{
		{&c.Valid, labNames, now.Add(-day), now.Add(90 * day), false},
		{&c.Expired, labNames, now.Add(-90 * day), now.Add(-2 * day), false},
		{&c.NotYet, labNames, now.Add(3 * day), now.Add(90 * day), false},
		{&c.Wrong, []string{"wrong.example.net"}, now.Add(-day), now.Add(90 * day), false},
		{&c.SelfSig, labNames, now.Add(-day), now.Add(90 * day), true},
		{&c.Soon, labNames, now.Add(-80 * day), now.Add(5 * day), false},
	}
	for _, l := range leaves {
		cert, err := c.leaf(l.names, l.from, l.to, l.selfSigned)
		if err != nil {
			return nil, err
		}
		*l.dst = cert
	}
	return c, nil
}

func (c *Certs) leaf(names []string, from, to time.Time, selfSigned bool) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("leaf key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    from,
		NotAfter:     to,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if names[0] != "wrong.example.net" {
		tmpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1)}
	}
	parent, signer := c.CA, any(c.CAKey)
	if selfSigned {
		parent, signer = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("leaf cert: %w", err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	return n
}
