package sharedtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// MTLSFiles holds paths to PEM files generated for a throwaway CA, a server cert for 127.0.0.1, and a
// client cert signed by that CA.
type MTLSFiles struct {
	CAFile         string
	ClientCertFile string
	ClientKeyFile  string
	ServerCertFile string
	ServerKeyFile  string
	ServerCert     tls.Certificate
	CAPool         *x509.CertPool
}

// NewMTLSFiles generates a CA and CA-signed server and client certificates in t.TempDir(). The server
// certificate is valid for 127.0.0.1.
func NewMTLSFiles(t *testing.T) MTLSFiles {
	t.Helper()
	return NewMTLSFilesInDir(t, t.TempDir(), nil, []net.IP{net.ParseIP("127.0.0.1")})
}

// NewMTLSFilesInDir is like NewMTLSFiles but writes the PEM files to dir and issues the server
// certificate for the given DNS names and IP addresses. The files are world-readable so that a
// container running as a different user can read them; use only for tests.
func NewMTLSFilesInDir(t *testing.T, dir string, serverDNSNames []string, serverIPs []net.IP) MTLSFiles {
	t.Helper()

	caKey, caCert, caDER := issueCert(t, nil, nil, "test-ca", true, nil, nil)

	srvKey, _, srvDER := issueCert(t, caCert, caKey, "server", false, serverDNSNames, serverIPs)
	cliKey, _, cliDER := issueCert(t, caCert, caKey, "client", false, nil, nil)

	srvKeyDER, err := x509.MarshalECPrivateKey(srvKey)
	require.NoError(t, err)
	cliKeyDER, err := x509.MarshalECPrivateKey(cliKey)
	require.NoError(t, err)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	srvCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER})
	srvKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: srvKeyDER})
	srvPair, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM))

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, data, 0o644)) //nolint:gosec // world-readable on purpose, see above
		return p
	}
	return MTLSFiles{
		CAFile:         write("ca.pem", caPEM),
		ClientCertFile: write("client.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cliDER})),
		ClientKeyFile:  write("client.key", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: cliKeyDER})),
		ServerCertFile: write("server.pem", srvCertPEM),
		ServerKeyFile:  write("server.key", srvKeyPEM),
		ServerCert:     srvPair,
		CAPool:         pool,
	}
}

func issueCert(
	t *testing.T,
	parent *x509.Certificate,
	parentKey *ecdsa.PrivateKey,
	cn string,
	isCA bool,
	dnsNames []string,
	ips []net.IP,
) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	if isCA {
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return key, cert, der
}

// StartMTLSPingServer starts a TLS listener on 127.0.0.1 that requires a client certificate signed by
// files.CAPool and answers every read with a Redis simple-string reply. It returns the listening port
// and a channel that receives one error (nil on success) per handshake attempt.
func StartMTLSPingServer(t *testing.T, files MTLSFiles) (port int, handshakes <-chan error) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{files.ServerCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    files.CAPool,
		MinVersion:   tls.VersionTLS12,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	ch := make(chan error, 100)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				err := c.(*tls.Conn).Handshake()
				select {
				case ch <- err:
				default:
				}
				if err != nil {
					return
				}
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
					_, _ = c.Write([]byte("+PONG\r\n"))
				}
			}(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, ch
}
