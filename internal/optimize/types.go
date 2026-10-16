// Package optimize runs format pipelines over files concurrently.
package optimize

import (
	"time"

	"github.com/klauspost/fox/internal/detect"
)

// State of a job.
type State string

const (
	Queued  State = "queued"
	Running State = "running"
	Done    State = "done"
	Skipped State = "skipped"
	Failed  State = "failed"
)

// Event reports a job change. Size is the current best size.
type Event struct {
	ID       int64         `json:"id"`
	Path     string        `json:"path"`
	Format   detect.Format `json:"format"`
	State    State         `json:"state"`
	Step     string        `json:"step,omitempty"` // e.g. "oxipng (6/16)"
	Original int64         `json:"original"`
	Size     int64         `json:"size"`
	Elapsed  time.Duration `json:"elapsed"`
	Message  string        `json:"message,omitempty"` // skip reason or error
	Upload   bool          `json:"upload,omitempty"`
}
