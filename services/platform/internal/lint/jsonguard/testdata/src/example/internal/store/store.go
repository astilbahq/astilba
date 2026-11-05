package store

import (
	"encoding/json" // want `encoding/json folds case`
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"io"
)

func Write(v any) ([]byte, error) {
	if _, err := json.Marshal(v); err != nil {
		return nil, err
	}
	return jsonv2.Marshal(v) // want `plain encoding/json/v2 output`
}

func Stream(w io.Writer) *jsontext.Encoder { return jsontext.NewEncoder(w) } // want `plain encoding/json/v2 output`

func Read(raw []byte, v any) error { return jsonv2.Unmarshal(raw, v) }
