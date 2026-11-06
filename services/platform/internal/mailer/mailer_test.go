package mailer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
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

// relay answers one message with endOfData, then drops the connection when asked to quit.
func relay(t *testing.T, endOfData string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		reply := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
		reply("220 test")
		inData := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case inData && line == ".\r\n":
				inData = false
				reply(endOfData)
			case inData:
			case strings.HasPrefix(line, "DATA"):
				inData = true
				reply("354 go ahead")
			case strings.HasPrefix(line, "QUIT"):
				return
			default:
				reply("250 ok")
			}
		}
	}()
	return listener.Addr().String()
}

// Callers delete a link or retry when told a message failed, so accepted mail must not be
// reported as failed, and refused mail must not be reported as sent.
func TestSendReportsWhatTheRelayDidWithTheMessage(t *testing.T) {
	for _, test := range []struct {
		name, endOfData string
		want            error
	}{
		{"accepted, then the connection drops at sign-off", "250 queued", nil},
		{"refused for now", "451 try later", ErrUnavailable},
		{"refused", "554 rejected", ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, err := New(Config{Address: relay(t, test.endOfData), From: "accounts@example.test", Mode: "plain", Development: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Send(context.Background(), "owner@example.test", "Subject", "Body"); !errors.Is(err, test.want) {
				t.Fatalf("Send returned %v, want %v", err, test.want)
			}
		})
	}
}
