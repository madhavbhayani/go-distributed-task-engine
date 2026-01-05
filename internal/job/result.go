package job

type Result struct {
	JobID        string
	Output       any
	ExecTimeMs   int64
	MemoryBytes  uint64
	Error        error
}