package finding

import "time"

type Finding struct {
	Type  string    `json:"type"`
	Port  int       `json:"port"`
	TS    time.Time `json:"ts"`
	JobID int       `json:"job_id,omitempty"`
}
