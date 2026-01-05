package job

import (
	"fmt"
	"time"
	
)

// JobType represents the type of job to execute
type JobType string

const (
    SumJob    JobType = "sum"
    SquareJob JobType = "square"
    PrimeJob  JobType = "prime"
)

// Job represents a task to be processed
type Job struct {
    ID       string
    Type     JobType
    Start    int
    End      int
    Priority int
}

func NewJob(jobType JobType, start int, end int, priority int) Job {
	return Job{
		ID:       generateJobID(),
		Type:     jobType,
		Start:    start,
		End:      end,
		Priority: priority,
	}
}

func generateJobID() string {
	return fmt.Sprintf("job-%d", time.Now().UnixNano())
}