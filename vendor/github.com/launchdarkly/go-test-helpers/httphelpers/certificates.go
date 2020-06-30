package httphelpers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"time"
)

// WithSelfSignedServer is a convenience function for starting a test HTTPS server with a self-signed
// certificate, running the specified function, and then closing the server and cleaning up the
// temporary certificate files. If for some reason creating the server fails, it panics. The action
// function's second and third parameters provide the CA certificate for configuring the client,
// and a preconfigured CertPool in case that is more convenient to use.
func WithSelfSignedServer(handler http.Handler, action func(*httptest.Server, []byte, *x509.CertPool)) {
	certFile, err := ioutil.TempFile("", "test")
	if err != nil {
		panic(fmt.Errorf("can't create temp file: %s", err))
	}
	_ = certFile.Close()
	certFilePath := certFile.Name()
	tryToDelete := func(path string) {
		err := os.Remove(path)
		if err != nil {
			log.Printf("Unable to clean up temp file %s: %s", path, err)
		}
	}
	defer tryToDelete(certFilePath)
	keyFile, err := ioutil.TempFile("", "test")
	if err != nil {
		panic(fmt.Errorf("can't create temp file: %s", err))
	}
	_ = keyFile.Close()
	keyFilePath := keyFile.Name()
	defer tryToDelete(keyFilePath)
	err = MakeSelfSignedCert(certFilePath, keyFilePath)
	if err != nil {
		panic(fmt.Errorf("can't create self-signed certificate: %s", err))
	}
	certData, err := ioutil.ReadFile(certFilePath) //nolint:gosec
	if err != nil {
		panic(fmt.Errorf("can't read self-signed certificate: %s", err))
	}
	certPool, err := x509.SystemCertPool()
	if err != nil {
		certPool = x509.NewCertPool() // necessary in order to work on Windows
	}
	certPool.AppendCertsFromPEM(certData)
	server, err := MakeServerWithCert(certFilePath, keyFilePath, handler)
	if err != nil {
		panic(fmt.Errorf("can't start HTTPS server: %s", err))
	}
	defer server.Close()
	defer server.CloseClientConnections()
	action(server, certData, certPool)
}

// MakeServerWithCert creates and starts a test HTTPS server using the specified certificate.
func MakeServerWithCert(certFilePath, keyFilePath string, handler http.Handler) (*httptest.Server, error) {
	cert, err := tls.LoadX509KeyPair(certFilePath, keyFilePath)
	if err != nil {
		return nil, err
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	server.TLS.BuildNameToCertificate() //nolint:staticcheck // method is deprecated but we still need to support older Gos
	server.StartTLS()
	return server, nil
}

// MakeSelfSignedCert generates a self-signed certificate and writes it to the specified files.
// See: https://golang.org/src/crypto/tls/generate_cert.go
func MakeSelfSignedCert(certFilePath, keyFilePath string) error {
	hosts := []string{"127.0.0.1"}
	isCA := true
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(time.Hour * 24)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Test"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}

	if isCA {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey(priv), priv)
	if err != nil {
		return err
	}

	certOut, err := os.Create(certFilePath)
	if err != nil {
		return err
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return err
	}
	if err := certOut.Close(); err != nil {
		return err
	}

	keyOut, err := os.OpenFile(keyFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	block, err := pemBlockForKey(priv)
	if err != nil {
		return err
	}
	if err := pem.Encode(keyOut, block); err != nil {
		return err
	}
	if err := keyOut.Close(); err != nil {
		return err
	}
	return nil
}

func pemBlockForKey(priv interface{}) (*pem.Block, error) {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}, nil
	case *ecdsa.PrivateKey:
		b, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		return &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}, nil
	default:
		return nil, nil
	}
}

func publicKey(priv interface{}) interface{} {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey
	case *ecdsa.PrivateKey:
		return &k.PublicKey
	default:
		return nil
	}
}
