package settings

import "time"

const maxRetentionDays = 3650

// CallRecords is how long the IMS keeps its call records.
type CallRecords struct {
	RetentionDays int
}

// Retention is how long a record is kept after its call was requested.
func (c CallRecords) Retention() time.Duration {
	return time.Duration(c.RetentionDays) * 24 * time.Hour
}

func (c CallRecords) Validate() error {
	if c.RetentionDays < 1 || c.RetentionDays > maxRetentionDays {
		return invalidf("call record retention must be 1 to %d days", maxRetentionDays)
	}

	return nil
}
