package terminal

import "testing"

func TestResizeBounds(t *testing.T) {
	if columns, rows, err := ParseResize(ResizeFrame(80, 24)); err != nil || columns != 80 || rows != 24 {
		t.Fatal(columns, rows, err)
	}
	for name, frame := range map[string][]byte{
		"no columns": ResizeFrame(0, 24),
		"no rows":    ResizeFrame(80, 0),
		"too wide":   ResizeFrame(1001, 24),
		"too tall":   ResizeFrame(80, 1001),
		"short":      ResizeFrame(80, 24)[:4],
		"wrong kind": {Input, 0, 80, 0, 24},
	} {
		if _, _, err := ParseResize(frame); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
