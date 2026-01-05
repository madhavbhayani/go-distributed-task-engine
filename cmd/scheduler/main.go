package main

import (
	"fmt"
	"os"
	"strconv"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/job"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/worker"
)

func main(){
	if len(os.Args) < 5 {
		fmt.Println("Usage:")
		fmt.Println("go run cmd/scheduler/main.go <jobType> <start> <end> <priority>")
		fmt.Println("Example:")
		fmt.Println("go run cmd/scheduler/main.go sum 1 100 5")
		return
	}
	jobTypeInput := os.Args[1]
	start, err1 := strconv.Atoi(os.Args[2])
	end, err2 := strconv.Atoi(os.Args[3])
	priority, err3 := strconv.Atoi(os.Args[4])
	if err1 != nil || err2 != nil || err3 != nil {
		fmt.Println("Error: start, end and priority must be integers")
		return
	}
	var jobType job.JobType

	switch jobTypeInput {
	case "sum":
		jobType = job.SumJob
	case "square":
		jobType = job.SquareJob
	case "prime":
		jobType = job.PrimeJob
	default:
		fmt.Println("Invalid job type. Use: sum | square | prime")
		return
	}

newJob := job.NewJob(jobType, start, end, priority)
jobChan := make(chan job.Job)
resultChan := make(chan job.Result)
workerCount := 5
for i := 1; i <= workerCount; i++ {
	go worker.StartWorker(i, jobChan, resultChan)
}

	fmt.Println("Job received by Scheduler:")
	fmt.Printf("ID: %s\n", newJob.ID)
	fmt.Printf("Type: %s\n", newJob.Type)
	fmt.Printf("Range: %d to %d\n", newJob.Start, newJob.End)
	fmt.Printf("Priority: %d\n", newJob.Priority)

	jobChan <- newJob
	close(jobChan)
	result := <-resultChan
	fmt.Println("\n=== JOB RESULT ===")
	fmt.Println("Job ID:", result.JobID)
	fmt.Println("Output:", result.Output)
	fmt.Println("Execution Time (ms):", result.ExecTimeMs)
	fmt.Println("Memory Used (bytes):", result.MemoryBytes)
}


	
