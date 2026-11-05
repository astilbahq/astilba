package stablejson

import (
	jsonv1 "encoding/json"
	"encoding/json/v2"
)

func Marshal(v any) ([]byte, error) { return json.Marshal(v, jsonv1.DefaultOptionsV1()) }
