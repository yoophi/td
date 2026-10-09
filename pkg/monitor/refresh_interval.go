package monitor

import "time"

// DefaultRefreshInterval is the CLI dashboard polling interval.
const DefaultRefreshInterval = time.Minute

// MinRefreshInterval bounds CLI polling and embedded GitHub dashboard polling.
const MinRefreshInterval = 30 * time.Second
