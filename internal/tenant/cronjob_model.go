package tenant

import "encoding/json"

// Cronjob represents a scheduled pipeline job definition from the compiled tenant config.
type Cronjob struct {
	Name     string         `json:"name"`
	Schedule string         `json:"schedule"`
	Enabled  bool           `json:"enabled"`
	Steps    []FunctionStep `json:"steps"`
}

// CronjobMap tolerates [] instead of {} for empty maps.
type CronjobMap map[string]Cronjob

func (m *CronjobMap) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*m = nil
		return nil
	}
	// Treat any array as an empty map.
	if len(data) > 0 && data[0] == '[' {
		*m = CronjobMap{}
		return nil
	}
	var result map[string]Cronjob
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	*m = CronjobMap(result)
	return nil
}
