package alerts

import "encoding/json/v2"

type Service struct{}

func (Service) send(v any) ([]byte, error) { return json.Marshal(v) }

type stored struct{}

func (stored) send(v any) ([]byte, error) { return json.Marshal(v) } // want `plain encoding/json/v2 output`

var marshal = json.Marshal // want `plain encoding/json/v2 output`

func save(v any) ([]byte, error) { return marshal(v) }
