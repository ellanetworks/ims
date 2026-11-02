package settings

import "time"

const maxRetentionDays = 3650

type CallRecords struct {
	RetentionDays int
}

func (c CallRecords) Retention() time.Duration {
	return time.Duration(c.RetentionDays) * 24 * time.Hour
}

func (c CallRecords) Validate() error {
	if c.RetentionDays < 1 || c.RetentionDays > maxRetentionDays {
		return invalidf("call record retention must be 1 to %d days", maxRetentionDays)
	}

	return nil
}
