package job

import (
	"errors"
	"runtime"
	"time"
)

func Execute(j Job) (Result, error) {

	startTime := time.Now()

	var memStart, memEnd runtime.MemStats
	runtime.ReadMemStats(&memStart)

	var res Result

	switch j.Type {

	case SumJob:
		res = executeSum(j)

	case SquareJob:
		res = executeSquare(j)

	case PrimeJob:
		res = executePrime(j)

	default:
		return Result{}, errors.New("unknown job type")
	}

	runtime.ReadMemStats(&memEnd)

	res.ExecTimeMs = time.Since(startTime).Milliseconds()
	res.MemoryBytes = memEnd.Alloc - memStart.Alloc

	return res, nil
}

func executeSum(j Job) Result {
	sum := 0
	for i := j.Start; i <= j.End; i++ {
		sum += i
	}

	return Result{
		JobID:  j.ID,
		Output: sum,
	}
}

func executeSquare(j Job) Result {
	squares := []int{}

	for i := j.Start; i <= j.End; i++ {
		squares = append(squares, i*i)
	}

	return Result{
		JobID:  j.ID,
		Output: squares,
	}
}

func executePrime(j Job) Result {
	primes := []int{}

	for i := j.Start; i <= j.End; i++ {
		if isPrime(i) {
			primes = append(primes, i)
		}
	}

	return Result{
		JobID:  j.ID,
		Output: primes,
	}
}

func isPrime(n int) bool {
	if n < 2 {
		return false
	}
	for i := 2; i*i <= n; i++ {
		if n%i == 0 {
			return false
		}
	}
	return true
}
