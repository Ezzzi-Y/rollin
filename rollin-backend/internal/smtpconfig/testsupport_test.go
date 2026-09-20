package smtpconfig

// Test-only helpers: in-memory schema for the smtp_config surface plus fake SMTP
// servers. fakeSMTPPort advertises STARTTLS and upgrades to a self-signed TLS session,
// mirroring real 587 submission; fakeTLSOnlyPort is implicit TLS from the first byte,
// mirroring 465. TestMain relaxes the client-side certificate check so the self-signed
// cert is accepted.

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/secretbox"
)

func TestMain(m *testing.M) {
	// The fake servers present a self-signed certificate; skip verification for every
	// client dial in this package's tests.
	tlsConfigFor = func(host string) *tls.Config {
		return &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	}
	os.Exit(m.Run())
}

func newSMTPTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	handle, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	schema := `
CREATE TABLE smtp_config (
  id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL, activity_id INTEGER NOT NULL DEFAULT 0,
  host TEXT NOT NULL, port INTEGER NOT NULL DEFAULT 587, encryption TEXT NOT NULL DEFAULT 'STARTTLS', username TEXT NOT NULL,
  password_cipher BLOB NOT NULL, from_address TEXT NOT NULL, verified_at DATETIME,
  config_version INTEGER NOT NULL DEFAULT 1, created_at DATETIME, updated_at DATETIME,
  UNIQUE (scope, activity_id)
);
CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL, activity_id INTEGER NOT NULL DEFAULT 0,
  actor_type TEXT NOT NULL, actor_user_id INTEGER, action TEXT NOT NULL,
  target_type TEXT, target_id INTEGER, change_summary TEXT, detail JSON,
  request_id TEXT, ip_address TEXT, user_agent TEXT, created_at DATETIME
);
`
	if err := handle.Exec(schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := handle.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return handle
}

func sealForTest(plaintext string) ([]byte, error) {
	return secretbox.Seal(make([]byte, 32), []byte(plaintext))
}

func errsIsNotConfigured(err error) bool {
	return errs.Is(err, errs.CodeSMTPNotConfigured)
}

var fakeServerCert = sync.OnceValue(func() tls.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
})

// fakeSMTPPort starts a minimal SMTP conversation server on 127.0.0.1 that advertises
// STARTTLS and upgrades the session to TLS, then accepts AUTH PLAIN / MAIL / RCPT /
// DATA / QUIT and drops the message.
func fakeSMTPPort(t *testing.T) int {
	t.Helper()
	return fakeSMTPListener(t, true)
}

// fakeSMTPListener is fakeSMTPPort with control over the STARTTLS advertisement:
// advertise=false exercises the client's mandatory-STARTTLS failure path.
func fakeSMTPListener(t *testing.T, advertiseSTARTTLS bool) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveFakeSMTP(conn, advertiseSTARTTLS)
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

// fakeTLSOnlyPort accepts connections that are TLS from the first byte (implicit TLS,
// the 465/SSL mode). The conversation after the handshake is identical.
func fakeTLSOnlyPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{fakeServerCert()}})
	go func() {
		for {
			conn, err := tlsListener.Accept()
			if err != nil {
				return
			}
			go serveFakeSMTP(conn, false)
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func serveFakeSMTP(conn net.Conn, advertiseSTARTTLS bool) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	write := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	write("220 localhost ESMTP rollin-fake")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.TrimRight(line, "\r\n"))
		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			write("250-localhost")
			if advertiseSTARTTLS {
				write("250-STARTTLS")
			}
			write("250-AUTH PLAIN")
			write("250 OK")
		case strings.HasPrefix(verb, "STARTTLS"):
			write("220 2.0.0 Ready to start TLS")
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{fakeServerCert()}})
			if herr := tlsConn.Handshake(); herr != nil {
				return
			}
			conn = tlsConn
			reader = bufio.NewReader(conn)
			write = func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
			advertiseSTARTTLS = false // the session is already encrypted
		case strings.HasPrefix(verb, "AUTH PLAIN"):
			parts := strings.SplitN(strings.TrimRight(line, "\r\n"), " ", 3)
			if len(parts) == 3 {
				if _, derr := base64.StdEncoding.DecodeString(parts[2]); derr != nil {
					write("535 authentication failed")
					return
				}
			}
			write("235 2.7.0 accepted")
		case strings.HasPrefix(verb, "DATA"):
			write("354 end with <CRLF>.<CRLF>")
			for {
				dataLine, derr := reader.ReadString('\n')
				if derr != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
			}
			write("250 OK queued")
		case strings.HasPrefix(verb, "QUIT"):
			write("221 bye")
			return
		default:
			write("250 OK")
		}
	}
}
