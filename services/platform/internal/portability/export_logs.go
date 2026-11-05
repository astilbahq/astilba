package portability

import (
	"encoding/json"
	"encoding/json/jsontext"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

// Public transfer excludes captured diagnostics. Internal recovery snapshots use
// the original rows, including observations and receipts, without this projection.
func omitExportLogs(table string, row Row) error {
	switch table {
	case "execution_operations":
		setValue(row, "note", "")
	case "supported_upgrades":
		var status map[string]jsontext.Value
		if json.Unmarshal(row["status"], &status) != nil {
			return ErrArchive
		}
		delete(status, "note")
		row["status"], _ = stablejson.Marshal(status)
	case "pipeline_runs":
		var result map[string]jsontext.Value
		if json.Unmarshal(row["result"], &result) != nil {
			return ErrArchive
		}
		delete(result, "logs")
		row["result"], _ = stablejson.Marshal(result)
	case "visual_runtime":
		var err error
		row["observation"], err = omitObservationLogs(row["observation"])
		return err
	case "cross_host_runtime":
		var inventory map[string]jsontext.Value
		if json.Unmarshal(row["inventory"], &inventory) != nil {
			return ErrArchive
		}
		if raw, ok := inventory["observation"]; ok {
			var err error
			inventory["observation"], err = omitObservationLogs(raw)
			if err != nil {
				return err
			}
		}
		row["inventory"], _ = stablejson.Marshal(inventory)
	case "cross_host_operations":
		var receipts map[string]map[string]jsontext.Value
		if json.Unmarshal(row["receipts"], &receipts) != nil {
			return ErrArchive
		}
		for _, receipt := range receipts {
			if raw, ok := receipt["observation"]; ok {
				var err error
				receipt["observation"], err = omitObservationLogs(raw)
				if err != nil {
					return err
				}
			}
		}
		row["receipts"], _ = stablejson.Marshal(receipts)
	}
	return nil
}

func omitObservationLogs(raw jsontext.Value) (jsontext.Value, error) {
	var observation map[string]jsontext.Value
	if json.Unmarshal(raw, &observation) != nil {
		return nil, ErrArchive
	}
	resources, ok := observation["resources"]
	if !ok {
		return raw, nil
	}
	var items []map[string]jsontext.Value
	if json.Unmarshal(resources, &items) != nil {
		return nil, ErrArchive
	}
	for _, resource := range items {
		delete(resource, "logs")
	}
	observation["resources"], _ = stablejson.Marshal(items)
	return stablejson.Marshal(observation)
}
