package sbitls

import "time"

// Advance moves the clock of c forward by d.
func Advance(c *Credentials, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	c.now = func() time.Time { return now.Add(d) }
}
