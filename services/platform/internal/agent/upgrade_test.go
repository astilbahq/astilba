package agent

import (
	"bytes"
	"strings"
	"testing"
)

func TestUpgradeComparisonPreservesCopyRecords(t *testing.T) {
	preamble := "-- PostgreSQL database dump\n-- Dumped from database version 17.10\n-- Dumped by pg_dump version 18.4\n\nSET statement_timeout = 0;\n"
	payload := "COPY public.records (value) FROM stdin;\n-- Dumped from database version 17.10\nSET not_a_statement\n\\.\n"
	var got bytes.Buffer
	w := upgradeSQLHeader{out: &got}
	for _, b := range []byte(preamble + payload) {
		if _, err := w.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	want := strings.Replace(preamble, "from database version 17.10", "from database version 18.4", 1) + payload
	if got.String() != want || !w.done {
		t.Fatal("archive comparison changed database data")
	}
}
