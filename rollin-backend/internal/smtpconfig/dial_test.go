package smtpconfig

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The dial-mode matrix: SSL must speak TLS from the first byte, STARTTLS must upgrade
// and must refuse a server that does not offer the extension, and the port-based
// default must resolve 465 → SSL.

func TestSendMailOverImplicitTLS(t *testing.T) {
	ctx := context.Background()
	port := fakeTLSOnlyPort(t)
	cfg := &Effective{
		Scope: "PLATFORM", Host: "127.0.0.1", Port: port,
		Encryption: EncryptionSSL, Username: "noreply@example.edu.cn",
		Password: "smtp-secret", From: "noreply@example.edu.cn",
	}
	if err := sendMail(ctx, cfg, "to@example.edu.cn", "s", "body", time.Minute); err != nil {
		t.Fatalf("SSL mode send: %v", err)
	}
}

func TestSTARTTLSMandatoryWhenAdvertised(t *testing.T) {
	ctx := context.Background()
	// Server does NOT advertise STARTTLS → the send must fail with a clear message
	// instead of silently submitting credentials in plaintext.
	port := fakeSMTPListener(t, false)
	cfg := &Effective{
		Scope: "PLATFORM", Host: "127.0.0.1", Port: port,
		Encryption: EncryptionSTARTTLS, Username: "noreply@example.edu.cn",
		Password: "smtp-secret", From: "noreply@example.edu.cn",
	}
	err := sendMail(ctx, cfg, "to@example.edu.cn", "s", "body", time.Minute)
	if err == nil {
		t.Fatal("expected STARTTLS refusal against a plaintext-only server")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("error should name STARTTLS, got: %v", err)
	}
}

func TestResolveEncryption(t *testing.T) {
	cases := []struct {
		in   string
		port int
		want string
	}{
		{"", 465, EncryptionSSL},
		{"", 587, EncryptionSTARTTLS},
		{"", 25, EncryptionSTARTTLS},
		{"ssl", 587, EncryptionSSL},
		{"starttls", 465, EncryptionSTARTTLS},
		{"none", 25, EncryptionNone},
	}
	for _, c := range cases {
		got, err := resolveEncryption(c.in, c.port)
		if err != nil || got != c.want {
			t.Fatalf("resolveEncryption(%q, %d) = %q, %v; want %q", c.in, c.port, got, err, c.want)
		}
	}
	if _, err := resolveEncryption("tls", 465); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
}
