package reads

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"time"
)

type nested struct {
	Name string `json:"name"`
	Port int
}

type Embedded struct {
	Host string `json:"host"`
}

type embedded struct {
	Hidden string
}

type custom struct{ Value string }

func (*custom) UnmarshalJSON([]byte) error { return nil }

// The shape behind the shadow decoder's finding in pipeline/provider.go: a plain import swap
// left Repository.ID zero, and every push webhook would have been refused.
func webhook(body []byte) {
	var data struct {
		Ref        string `json:"ref"`
		Repository struct{ ID int64 }
	}
	json.Unmarshal(body, &data) // want `field Repository is read from JSON with no json tag` `field ID is read from JSON with no json tag`
}

func decoder(r io.Reader) {
	var out []map[string]*nested
	json.NewDecoder(r).Decode(&out) // want `field Port is read`
}

func v2Readers(body []byte, r io.Reader) {
	var first struct{ ID int64 }
	jsonv2.Unmarshal(body, &first) // want `field ID is read`
	var second struct{ ID int64 }
	jsonv2.UnmarshalRead(r, &second) // want `field ID is read`
}

func unexportedEmbedded(body []byte) {
	var data struct{ embedded }
	json.Unmarshal(body, &data) // want `field Hidden is read`
}

// A client's request method decodes into whatever its callers pass.
func request(client *http.Client, path string, out any) error {
	response, err := client.Get(path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return json.NewDecoder(response.Body).Decode(out)
}

func throughRequest(client *http.Client) {
	var review struct {
		URL   string `json:"url"`
		State string
	}
	request(client, "/review", &review) // want `field State is read`
}

// A journal reads back the Go field names it wrote itself.
func journalGet(raw []byte, out any) error { return json.Unmarshal(raw, out) }

func throughJournal(raw []byte) {
	var record struct{ Phase string }
	journalGet(raw, &record)
}

func spreadArguments(read func() ([]byte, any)) {
	json.Unmarshal(read())
}

func accepted(body []byte) {
	var data struct {
		Name    string    `json:"name,omitempty"`
		Secret  string    `json:"-"`
		At      time.Time `json:"at"`
		Reads   custom    `json:"reads"`
		Rest    Embedded  `json:",embed"`
		private string
	}
	_ = data.private
	json.Unmarshal(body, &data)
	var loose map[string]any
	json.Unmarshal(body, &loose)
	var unknown any = &loose
	json.Unmarshal(body, unknown)
}

func omittedName(body []byte) {
	var data struct {
		Count int `json:",omitempty"`
	}
	json.Unmarshal(body, &data) // want `field Count is read`
}
