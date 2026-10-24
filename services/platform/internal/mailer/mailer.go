package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type Config struct {
	Address, Username, Password, From, Mode string
	Development                             bool
}
type Mailer struct{ config Config }

var ErrUnavailable = errors.New("email is unavailable; configure SMTP and retry")

func New(c Config) (*Mailer, error) {
	if c.Address == "" {
		return &Mailer{config: c}, nil
	}
	host, _, err := net.SplitHostPort(c.Address)
	if err != nil {
		return nil, ErrUnavailable
	}
	if strings.ContainsAny(c.From, "\r\n") {
		return nil, ErrUnavailable
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return nil, ErrUnavailable
	}
	c.From = from.Address
	if c.Mode != "starttls" && c.Mode != "tls" && !(c.Mode == "plain" && c.Development && (host == "localhost" || host == "127.0.0.1" || host == "::1")) {
		return nil, ErrUnavailable
	}
	return &Mailer{config: c}, nil
}
func (m *Mailer) Send(ctx context.Context, to, subject, body string) error {
	c := m.config
	if c.Address == "" {
		return ErrUnavailable
	}
	if strings.ContainsAny(to+subject, "\r\n") {
		return ErrUnavailable
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return ErrUnavailable
	}
	host, _, _ := net.SplitHostPort(c.Address)
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return ErrUnavailable
	}
	defer conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if c.Mode == "tls" {
		secured := tls.Client(conn, tlsConfig)
		if secured.HandshakeContext(ctx) != nil {
			return ErrUnavailable
		}
		conn = secured
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return ErrUnavailable
	}
	defer client.Close()
	if c.Mode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return ErrUnavailable
		}
		if client.StartTLS(tlsConfig) != nil {
			return ErrUnavailable
		}
	}
	if c.Username != "" {
		if client.Auth(smtp.PlainAuth("", c.Username, c.Password, host)) != nil {
			return ErrUnavailable
		}
	}
	if client.Mail(c.From) != nil || client.Rcpt(to) != nil {
		return ErrUnavailable
	}
	w, err := client.Data()
	if err != nil {
		return ErrUnavailable
	}
	_, err = fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", c.From, to, subject, body)
	if err != nil {
		return ErrUnavailable
	}
	if w.Close() != nil {
		return ErrUnavailable
	}
	if client.Quit() != nil {
		return ErrUnavailable
	}
	return nil
}
