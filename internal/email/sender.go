package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type NoopSender struct{}

func (NoopSender) Send(context.Context, Message) (string, error) {
	return "noop:" + time.Now().UTC().Format("20060102150405.000000000"), nil
}

type SMTPSender struct {
	Addr     string
	Host     string
	Username string
	Password string
	From     string
	UseTLS   bool
}

func (s SMTPSender) Send(ctx context.Context, message Message) (string, error) {
	if strings.TrimSpace(s.Addr) == "" || strings.TrimSpace(s.From) == "" {
		return "", fmt.Errorf("smtp addr and from are required")
	}
	host := s.Host
	if host == "" {
		host, _, _ = net.SplitHostPort(s.Addr)
	}
	body := "From: " + s.From + "\r\n" +
		"To: " + message.To + "\r\n" +
		"Subject: " + message.Subject + "\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		message.BodyText
	auth := smtp.Auth(nil)
	if s.Username != "" {
		auth = smtp.PlainAuth("", s.Username, s.Password, host)
	}
	done := make(chan error, 1)
	go func() {
		if s.UseTLS {
			done <- sendMailTLS(s.Addr, auth, s.From, []string{message.To}, []byte(body))
			return
		}
		done <- smtp.SendMail(s.Addr, auth, s.From, []string{message.To}, []byte(body))
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			return "", err
		}
		return "smtp:" + time.Now().UTC().Format("20060102150405.000000000"), nil
	}
}

func sendMailTLS(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	defer conn.Close()
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if auth != nil {
		if err = client.Auth(auth); err != nil {
			return err
		}
	}
	if err = client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range to {
		if err = client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = writer.Write(msg); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}
