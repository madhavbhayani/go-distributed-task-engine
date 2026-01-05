package main

import (
	"fmt"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/job"
)

func main() {
	fmt.Println("Worker started")

	// TEMP: Simulated job (scheduler will send later)
	j := job.NewJob(job.PrimeJob, 1, 100000, 3)

	result, err := job.Execute(j)
	if err != nil {
		fmt.Println("Execution failed:", err)
		return
	}

	fmt.Println("Job ID:", result.JobID)
	fmt.Println("Output:", result.Output)
	fmt.Println("Execution Time (ms):", result.ExecTimeMs)
	fmt.Println("Memory Used (bytes):", result.MemoryBytes)
}
