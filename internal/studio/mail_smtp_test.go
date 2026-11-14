package studio

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mailSMTPOptions struct {
	TLSMode        string
	NoSTARTTLS     bool
	RCPTReply      string
	FinalReply     string
	DropAfterData  bool
	StallAfterData bool
	StallGreeting  bool
	HugeGreeting   bool
}

type mailSMTPFixture struct {
	Config      MailConfig
	Roots       *x509.CertPool
	CAPEM       []byte // Public fixture certificate only; never contains the private key.
	Messages    chan []byte
	Connections atomic.Int32
	AuthCalls   atomic.Int32
	DataCalls   atomic.Int32
	listener    net.Listener
	closed      chan struct{}
	mu          sync.Mutex
	connections map[net.Conn]bool
	wg          sync.WaitGroup
}

// Real loopback sockets and TLS; no production certificate bypass is exposed.
// Messages are only held in test memory. Do not print them on assertion errors.
func newMailSMTPFixture(t *testing.T, options mailSMTPOptions) *mailSMTPFixture {
	t.Helper()
	if options.TLSMode == "" {
		options.TLSMode = "implicit_tls"
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mail fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(parsed)
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	f := &mailSMTPFixture{Config: MailConfig{Enabled: true, Host: "127.0.0.1", Port: port, TLSMode: options.TLSMode, Username: "fixture-user", Password: "fixture-password", From: "security@example.test", PublicOrigin: "http://127.0.0.1:5173", OutboxKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), Timeout: 5 * time.Second, Workers: 1}, Roots: roots, CAPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), Messages: make(chan []byte, 64), listener: listener, closed: make(chan struct{}), connections: make(map[net.Conn]bool)}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			f.Connections.Add(1)
			f.mu.Lock()
			f.connections[raw] = true
			f.mu.Unlock()
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				defer raw.Close()
				defer func() { f.mu.Lock(); delete(f.connections, raw); f.mu.Unlock() }()
				f.serve(raw, tlsConfig, options)
			}()
		}
	}()
	t.Cleanup(func() {
		close(f.closed)
		_ = listener.Close()
		f.mu.Lock()
		for c := range f.connections {
			_ = c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func (f *mailSMTPFixture) sender() *smtpMailSender {
	s := newSMTPMailSender(f.Config)
	s.tlsConfig.RootCAs = f.Roots
	return s
}

func (f *mailSMTPFixture) serve(raw net.Conn, tlsConfig *tls.Config, options mailSMTPOptions) {
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	var conn net.Conn = raw
	secure := options.TLSMode == "implicit_tls"
	if secure {
		conn = tls.Server(raw, tlsConfig)
	}
	if options.StallGreeting {
		<-f.closed
		return
	}
	p := textproto.NewConn(conn)
	if options.HugeGreeting {
		_, _ = io.WriteString(conn, "220 "+strings.Repeat("x", 512<<10)+"\r\n")
		return
	}
	if p.PrintfLine("220 fixture ESMTP") != nil {
		return
	}
	for {
		line, err := p.ReadLine()
		if err != nil {
			return
		}
		command, _, _ := strings.Cut(line, " ")
		switch strings.ToUpper(command) {
		case "EHLO", "HELO":
			_ = p.PrintfLine("250-fixture")
			if !secure && !options.NoSTARTTLS {
				_ = p.PrintfLine("250-STARTTLS")
			}
			_ = p.PrintfLine("250 AUTH PLAIN")
		case "STARTTLS":
			if options.NoSTARTTLS {
				return
			}
			_ = p.PrintfLine("220 Ready")
			tlsConn := tls.Server(raw, tlsConfig)
			if tlsConn.Handshake() != nil {
				return
			}
			p = textproto.NewConn(tlsConn)
			secure = true
		case "AUTH":
			f.AuthCalls.Add(1)
			if !secure {
				_ = p.PrintfLine("538 TLS required")
				return
			}
			_ = p.PrintfLine("235 Authenticated")
		case "MAIL":
			_ = p.PrintfLine("250 Sender accepted")
		case "RCPT":
			if options.RCPTReply != "" {
				_ = p.PrintfLine("%s", options.RCPTReply)
			} else {
				_ = p.PrintfLine("250 Recipient accepted")
			}
		case "DATA":
			_ = p.PrintfLine("354 Continue")
			data, err := p.ReadDotBytes()
			if err != nil {
				return
			}
			f.DataCalls.Add(1)
			select {
			case f.Messages <- data:
			default:
			}
			if options.DropAfterData {
				return
			}
			if options.StallAfterData {
				<-f.closed
				return
			}
			if options.FinalReply != "" {
				_ = p.PrintfLine("%s", options.FinalReply)
			} else {
				_ = p.PrintfLine("250 Accepted")
			}
		case "QUIT":
			_ = p.PrintfLine("221 Bye")
			return
		default:
			_ = p.PrintfLine("500 Unsupported")
		}
	}
}

func testMailEnvelope() EmailEnvelope {
	return EmailEnvelope{ID: "mail_fixture_1", Purpose: "reset_password", To: "member@example.test", Token: strings.Repeat("A", 43), ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}
}

func testMailSealed(t *testing.T, c MailConfig) (encryptedMail, mailPayload) {
	t.Helper()
	crypt, err := newMailCipher(c.OutboxKey)
	if err != nil {
		t.Fatal(err)
	}
	m := &mailDelivery{config: c, cipher: crypt}
	item, err := m.seal(testMailEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	p, err := crypt.open(item)
	if err != nil {
		t.Fatal(err)
	}
	return item, p
}

func mailFixtureBody(t *testing.T, raw []byte) string {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal("invalid fixture MIME")
	}
	body, err := io.ReadAll(quotedprintable.NewReader(m.Body))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestMailSMTPImplicitAndSTARTTLS(t *testing.T) {
	for _, mode := range []string{"implicit_tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			f := newMailSMTPFixture(t, mailSMTPOptions{TLSMode: mode})
			item, p := testMailSealed(t, f.Config)
			result := f.sender().Send(context.Background(), item, p)
			if result.State != "SENT" || f.AuthCalls.Load() != 1 || f.DataCalls.Load() != 1 {
				t.Fatalf("SMTP result=%+v auth=%d data=%d", result, f.AuthCalls.Load(), f.DataCalls.Load())
			}
			body := mailFixtureBody(t, <-f.Messages)
			if !strings.Contains(body, "/#auth=reset&token="+p.Token) || strings.Contains(body, "?token=") {
				t.Fatal("security link did not use the trusted fragment")
			}
		})
	}
}

func TestMailSMTPAcceptanceAndSafeFailureClasses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options mailSMTPOptions
		want    string
	}{
		{"predata4xx", mailSMTPOptions{RCPTReply: "451 fixture recipient rejected"}, "RETRY"},
		{"predata5xx", mailSMTPOptions{RCPTReply: "550 fixture recipient rejected"}, "FAILED"},
		{"data4xx", mailSMTPOptions{FinalReply: "451 fixture rejected body"}, "RETRY"},
		{"data5xx", mailSMTPOptions{FinalReply: "550 fixture rejected body"}, "FAILED"},
		{"lostfinal", mailSMTPOptions{DropAfterData: true}, "SUBMIT_UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMailSMTPFixture(t, tc.options)
			item, p := testMailSealed(t, f.Config)
			result := f.sender().Send(context.Background(), item, p)
			if result.State != tc.want {
				t.Fatalf("result=%+v", result)
			}
			if strings.Contains(result.Code, "fixture") || strings.Contains(result.Code, p.Token) || strings.Contains(result.Code, p.To) {
				t.Fatal("unsafe SMTP diagnostics")
			}
		})
	}
}

func TestMailSMTPRejectsDowngradeAndUntrustedCertificate(t *testing.T) {
	t.Run("no-starttls", func(t *testing.T) {
		f := newMailSMTPFixture(t, mailSMTPOptions{TLSMode: "starttls", NoSTARTTLS: true})
		item, p := testMailSealed(t, f.Config)
		result := f.sender().Send(context.Background(), item, p)
		if result.Code != "SMTP_TLS_REQUIRED" || f.AuthCalls.Load() != 0 || f.DataCalls.Load() != 0 {
			t.Fatalf("downgrade accepted: %+v", result)
		}
	})
	t.Run("unknown-ca", func(t *testing.T) {
		f := newMailSMTPFixture(t, mailSMTPOptions{})
		item, p := testMailSealed(t, f.Config)
		result := newSMTPMailSender(f.Config).Send(context.Background(), item, p)
		if result.Code != "SMTP_TLS_FAILED" || f.AuthCalls.Load() != 0 {
			t.Fatalf("certificate was not verified: %+v", result)
		}
	})
}

func TestMailSMTPCancellationClassification(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(strconv.FormatBool(after), func(t *testing.T) {
			f := newMailSMTPFixture(t, mailSMTPOptions{StallAfterData: after, StallGreeting: !after})
			item, p := testMailSealed(t, f.Config)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			result := f.sender().Send(ctx, item, p)
			if time.Since(start) > time.Second {
				t.Fatal("SMTP ignored context deadline")
			}
			if after && result.State != "SUBMIT_UNKNOWN" {
				t.Fatalf("post-DATA cancellation=%+v", result)
			}
			if !after && result.State == "SUBMIT_UNKNOWN" {
				t.Fatal("pre-DATA cancellation should be retryable")
			}
			if !after && result.State != "RETRY" {
				t.Fatalf("pre-DATA network timeout must remain retryable: %+v", result)
			}
		})
	}
}

func TestMailSMTPBoundsServerReplies(t *testing.T) {
	f := newMailSMTPFixture(t, mailSMTPOptions{HugeGreeting: true})
	item, p := testMailSealed(t, f.Config)
	result := f.sender().Send(context.Background(), item, p)
	if result.State != "RETRY" || f.AuthCalls.Load() != 0 || f.DataCalls.Load() != 0 {
		t.Fatalf("unbounded greeting accepted: %+v", result)
	}
}
