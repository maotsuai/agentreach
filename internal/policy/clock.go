package policy

import "time"

// Stamp is comparable across processes on one boot. Wall is only a rollback
// observation; it never extends or decides an approval's monotonic deadline.
type Stamp struct {
	Boot string `json:"boot"`
	Tick int64  `json:"tick"`
	Wall int64  `json:"wall"`
}

// Clock returns a native, boot-bound monotonic stamp.
type Clock func() (Stamp, error)

func wallNow() int64 { return time.Now().UnixNano() }
