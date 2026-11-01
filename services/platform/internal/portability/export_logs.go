package portability

import "encoding/json"

// Public transfer excludes captured diagnostics. Internal recovery snapshots use
// the original rows, including observations and receipts, without this projection.
func omitExportLogs(table string, row Row) error {
	switch table {
	case "execution_operations":
		setValue(row, "note", "")
	case "supported_upgrades":
		var status map[string]json.RawMessage
		if json.Unmarshal(row["status"], &status) != nil {
			return ErrArchive
		}
		delete(status, "note")
		row["status"], _ = json.Marshal(status)
	case "pipeline_runs":
		var result map[string]json.RawMessage
		if json.Unmarshal(row["result"], &result) != nil {
			return ErrArchive
		}
		delete(result, "logs")
		row["result"], _ = json.Marshal(result)
	case "visual_runtime":
		var err error
		row["observation"], err = omitObservationLogs(row["observation"])
		return err
	case "cross_host_runtime":
		var inventory map[string]json.RawMessage
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
		row["inventory"], _ = json.Marshal(inventory)
	case "cross_host_operations":
		var receipts map[string]map[string]json.RawMessage
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
		row["receipts"], _ = json.Marshal(receipts)
	}
	return nil
}

func omitObservationLogs(raw json.RawMessage) (json.RawMessage, error) {
	var observation map[string]json.RawMessage
	if json.Unmarshal(raw, &observation) != nil {
		return nil, ErrArchive
	}
	resources, ok := observation["resources"]
	if !ok {
		return raw, nil
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(resources, &items) != nil {
		return nil, ErrArchive
	}
	for _, resource := range items {
		delete(resource, "logs")
	}
	observation["resources"], _ = json.Marshal(items)
	return json.Marshal(observation)
}
