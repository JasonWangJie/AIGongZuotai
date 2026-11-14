package studio

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type mailSendResult struct{ State, Code string }
type mailSender interface {
	Send(context.Context, encryptedMail, mailPayload) mailSendResult
}

type smtpMailSender struct {
	config    MailConfig
	tlsConfig *tls.Config
}

func newSMTPMailSender(c MailConfig) *smtpMailSender {
	c = c.normalized()
	return &smtpMailSender{config: c, tlsConfig: &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}}
}

// Bound total server replies as well as time; a hostile/incorrect SMTP endpoint
// cannot stream unlimited textproto lines into memory before a deadline.
type mailReadBound struct {
	net.Conn
	remaining int
}

func (c *mailReadBound) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errors.New("SMTP response exceeds bound")
	}
	if len(p) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.Conn.Read(p)
	c.remaining -= n
	return n, err
}

func smtpFailure(err error, afterData bool) mailSendResult {
	var reply *textproto.Error
	if errors.As(err, &reply) {
		if reply.Code >= 400 && reply.Code < 500 {
			return mailSendResult{"RETRY", "SMTP_TEMPORARY_REJECTION"}
		}
		if reply.Code >= 500 && reply.Code < 600 {
			return mailSendResult{"FAILED", "SMTP_PERMANENT_REJECTION"}
		}
	}
	if afterData {
		return mailSendResult{"SUBMIT_UNKNOWN", "SMTP_ACCEPTANCE_UNKNOWN"}
	}
	return mailSendResult{"RETRY", "SMTP_CONNECTION_UNAVAILABLE"}
}

func smtpTLSFailure(err error) mailSendResult {
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var record tls.RecordHeaderError
	if errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname) || errors.As(err, &record) {
		return mailSendResult{"FAILED", "SMTP_TLS_FAILED"}
	}
	return smtpFailure(err, false)
}

func (s *smtpMailSender) Send(parent context.Context, item encryptedMail, payload mailPayload) mailSendResult {
	body, err := renderSecurityMail(item, payload)
	if err != nil {
		return mailSendResult{"FAILED", "MAIL_PAYLOAD_INVALID"}
	}
	ctx, cancel := context.WithTimeout(parent, s.config.Timeout)
	defer cancel()
	dialer := net.Dialer{}
	raw, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port)))
	if err != nil {
		return smtpFailure(err, false)
	}
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = raw.SetDeadline(deadline)
	var conn net.Conn = &mailReadBound{Conn: raw, remaining: 256 << 10}
	if s.config.TLSMode == "implicit_tls" {
		tlsConn := tls.Client(conn, s.tlsConfig.Clone())
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			return smtpTLSFailure(err)
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, s.config.Host)
	if err != nil {
		return smtpFailure(err, false)
	}
	defer client.Close()
	// Extension() discards hello errors. Send it explicitly so a transient
	// disconnect is not mislabeled as permanent unsupported TLS/AUTH.
	if err = client.Hello("localhost"); err != nil {
		return smtpFailure(err, false)
	}
	if s.config.TLSMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return mailSendResult{"FAILED", "SMTP_TLS_REQUIRED"}
		}
		if err = client.StartTLS(s.tlsConfig.Clone()); err != nil {
			return smtpTLSFailure(err)
		}
	}
	state, secure := client.TLSConnectionState()
	if !secure || !state.HandshakeComplete || state.Version < tls.VersionTLS12 {
		return mailSendResult{"FAILED", "SMTP_TLS_REQUIRED"}
	}
	if ok, methods := client.Extension("AUTH"); !ok || !mailHasAuthPlain(methods) {
		return mailSendResult{"FAILED", "SMTP_AUTH_UNSUPPORTED"}
	}
	if err = client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)); err != nil {
		result := smtpFailure(err, false)
		if result.State != "RETRY" {
			result.Code = "SMTP_AUTH_REJECTED"
		}
		return result
	}
	if err = client.Mail(payload.From); err != nil {
		return smtpFailure(err, false)
	}
	if err = client.Rcpt(payload.To); err != nil {
		return smtpFailure(err, false)
	}
	writer, err := client.Data()
	if err != nil {
		return smtpFailure(err, false)
	}
	// Once DATA writing starts, loss of confirmation is conservatively unknown.
	// Do not close the DATA writer on a Write failure: Close sends the terminator.
	if _, err = writer.Write(body); err != nil {
		return smtpFailure(err, true)
	}
	if err = writer.Close(); err != nil {
		return smtpFailure(err, true)
	}
	// DATA Close consumed final 250. QUIT/connection shutdown cannot retract it.
	return mailSendResult{"SENT", ""}
}

func mailHasAuthPlain(methods string) bool {
	for _, v := range strings.Fields(methods) {
		if strings.EqualFold(v, "PLAIN") {
			return true
		}
	}
	return false
}

func renderSecurityMail(item encryptedMail, p mailPayload) ([]byte, error) {
	if !validMailIdentity(item) || !mailTokenPattern.MatchString(p.Token) {
		return nil, errors.New("invalid mail payload")
	}
	if _, err := strictMailbox(p.To); err != nil {
		return nil, err
	}
	if _, err := strictMailbox(p.From); err != nil {
		return nil, err
	}
	origin, err := mailOrigin(p.Origin)
	if err != nil {
		return nil, err
	}
	action, title := "verify", "验证你的星图工作台邮箱"
	if item.Purpose == "reset_password" {
		action, title = "reset", "重置你的星图工作台密码"
	}
	fragment := url.Values{"auth": {action}, "token": {p.Token}}
	origin.Fragment = fragment.Encode()
	text := "请打开下方链接完成操作。链接只能使用一次，到期时间：" + item.ExpiresAt.UTC().Format(time.RFC3339) + "。\r\n\r\n" + origin.String() + "\r\n\r\n如果你没有请求此操作，请忽略此邮件。不要把此链接或令牌转发给他人。\r\n"
	var out bytes.Buffer
	// User input cannot alter headers. Message-ID is stable across safe retries.
	out.WriteString("From: " + p.From + "\r\nTo: " + p.To + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", title) + "\r\n")
	out.WriteString("Message-ID: <" + item.ID + "@" + origin.Hostname() + ">\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	writer := quotedprintable.NewWriter(&out)
	if _, err = io.WriteString(writer, text); err != nil {
		return nil, errors.New("mail rendering failed")
	}
	if err = writer.Close(); err != nil {
		return nil, errors.New("mail rendering failed")
	}
	if out.Len() > 64<<10 {
		return nil, errors.New("mail rendering exceeds bound")
	}
	return out.Bytes(), nil
}
