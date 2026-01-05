package worker
import (
	"github.com/madhavbhayani/go-distributed-task-engine/internal/job"
)

func StartWorker(id int, jobChan <- chan job.Job, resultChan chan <- job.Result) {

	for j := range jobChan { 
		result, err := job.Execute(j)
				if err != nil {
				resultChan <- job.Result{
				JobID: j.ID,
				Error: err,
			}
			continue
		}
		resultChan <- result
}
}