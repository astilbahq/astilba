package mailer

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestConsoleDeliveryIsExplicitAndDevelopmentOnly(t *testing.T) {
	for _, c := range []Config{
		{Mode: "console", From: "accounts@localhost"},
		{Mode: "console", Development: true, Address: "smtp.example.test:587", From: "accounts@localhost"},
		{Mode: "console", Development: true, Username: "smtp-user", From: "accounts@localhost"},
		{Mode: "console", Development: true, Password: "smtp-password", From: "accounts@localhost"},
	} {
		if _, err := New(c); err == nil {
			t.Fatal("console delivery accepted outside development or alongside SMTP connection settings")
		}
	}
	for _, development := range []bool{false, true} {
		m, err := New(Config{Development: development})
		if err != nil {
			t.Fatal(err)
		}
		if m.console != nil || !errors.Is(m.Send(context.Background(), "owner@example.test", "Reset", "private token"), ErrUnavailable) {
			t.Fatal("missing SMTP must not fall back to console delivery")
		}
	}
}

func TestConsoleEmailIncludesPlaintextAndReportsWriteFailure(t *testing.T) {
	m, err := New(Config{Mode: "console", Development: true, From: "Astilba <accounts@localhost>"})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	m.console = &output
	body := "Open this link:\n\nhttp://127.0.0.1:54182/reset?token=local-test-token"
	if err := m.Send(context.Background(), "owner@example.test", "Reset your password", body); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Local development email", "From: accounts@localhost", "To: owner@example.test", "Subject: Reset your password", body} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("console email omitted %q", text)
		}
	}
	m.console = failingWriter{}
	if !errors.Is(m.Send(context.Background(), "owner@example.test", "Reset", body), ErrUnavailable) {
		t.Fatal("failed console write was reported as successful delivery")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed output") }
