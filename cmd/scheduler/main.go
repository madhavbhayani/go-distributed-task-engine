package main

import (
	"fmt"
	"os"
	// "strconv"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/job"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/worker"
)

func main(){
	if len(os.Args) < 1 {
		fmt.Println("Usage:")
		fmt.Println("go run cmd/scheduler/main.go")
		fmt.Println("Example:")
		fmt.Println("go run cmd/scheduler/main.go")
		return
	}
	
	fmt.Print("Enter number of workers: ")
	var workerCount int
	fmt.Scanln(&workerCount)

	fmt.Print("Enter number of jobs: ")
	var jobCount int
	fmt.Scanln(&jobCount)

jobs := make([]job.Job, 0, jobCount)
for i := 1; i <= jobCount; i++ {

	fmt.Printf("Enter details for Job %d (type start end priority): ", i)
	var jobTypeInput string
	var start, end, priority int
	fmt.Scanln(&jobTypeInput, &start, &end, &priority)

	var jobType job.JobType
	switch jobTypeInput {
	case "sum":
		jobType = job.SumJob
	case "square":
		jobType = job.SquareJob
	case "prime":
		jobType = job.PrimeJob
	default:
		fmt.Println("Invalid job type")
		i--
		continue
	}

	jobs = append(jobs, job.NewJob(jobType, start, end, priority))
}


// start, err1 := strconv.Atoi(os.Args[2])
// end, err2 := strconv.Atoi(os.Args[3])
// priority, err3 := strconv.Atoi(os.Args[4])
// if err1 != nil || err2 != nil || err3 != nil {
// 	fmt.Println("Error: start, end and priority must be integers")
// 	return
// }
jobChan := make(chan job.Job)
resultChan := make(chan job.Result)

for i := 1; i <= workerCount; i++ {
	go worker.StartWorker(i, jobChan, resultChan)
}


for _, j := range jobs {
	fmt.Println("Job received by Scheduler:")
	fmt.Printf("ID: %s\n", j.ID)
	fmt.Printf("Type: %s\n", j.Type)
	fmt.Printf("Range: %d to %d\n", j.Start, j.End)
	fmt.Printf("Priority: %d\n", j.Priority)
	jobChan <- j
}

	close(jobChan)
	for i := 0; i < len(jobs); i++ {
	result := <-resultChan

	fmt.Println("\n=== JOB RESULT ===")
	fmt.Println("Job ID:", result.JobID)
	fmt.Println("Output:", result.Output)
	fmt.Println("Execution Time (ms):", result.ExecTimeMs)
	fmt.Println("Memory Used (bytes):", result.MemoryBytes)
}
}


	
