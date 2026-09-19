package domain

import "time"

type OperationStatus string

const (
	Pending      OperationStatus = "PENDING"
	Queued       OperationStatus = "QUEUED"
	Running      OperationStatus = "RUNNING"
	Succeeded    OperationStatus = "SUCCEEDED"
	Failed       OperationStatus = "FAILED"
	Retrying     OperationStatus = "RETRYING"
	DeadLettered OperationStatus = "DEAD_LETTERED"
	Cancelled    OperationStatus = "CANCELLED"
)

var transitions = map[OperationStatus]map[OperationStatus]bool{
	Pending:  {Queued: true, Cancelled: true},
	Queued:   {Running: true, Cancelled: true},
	Running:  {Succeeded: true, Failed: true, Retrying: true, DeadLettered: true, Cancelled: true},
	Failed:   {Retrying: true, DeadLettered: true, Cancelled: true},
	Retrying: {Queued: true, Running: true, DeadLettered: true, Cancelled: true},
}

func CanTransition(from, to OperationStatus) bool { return transitions[from][to] }
func IsTerminal(status OperationStatus) bool {
	return status == Succeeded || status == DeadLettered || status == Cancelled
}

func RetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<attempt) * time.Second
}
