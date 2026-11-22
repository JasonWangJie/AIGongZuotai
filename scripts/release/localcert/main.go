// Generate a fresh, short-lived certificate for the isolated loopback drill.
// The orchestrator receives the key on a private pipe and never logs it.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"os"
	"time"
)

func main() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal("could not generate loopback key")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		log.Fatal("could not generate certificate serial")
	}
	now := time.Now()
	template := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Isolated Workbench Loopback"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		log.Fatal("could not issue loopback certificate")
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		log.Fatal("could not encode loopback key")
	}
	err = json.NewEncoder(os.Stdout).Encode(map[string]string{
		"cert": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		"key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})),
	})
	if err != nil {
		log.Fatal("could not transfer loopback certificate")
	}
}
