package domain

import (
	"testing"
	"time"
)

func TestTerminalStatesAreImmutable(t *testing.T) {
	for _, from := range []OperationStatus{Succeeded, DeadLettered, Cancelled} {
		for _, to := range []OperationStatus{Pending, Queued, Running, Succeeded, Failed, Retrying, DeadLettered, Cancelled} {
			if CanTransition(from, to) {
				t.Fatalf("terminal state %s transitioned to %s", from, to)
			}
		}
	}
}

func TestExpectedLifecycle(t *testing.T) {
	path := []OperationStatus{Pending, Queued, Running, Retrying, Queued, Running, Succeeded}
	for i := 0; i < len(path)-1; i++ {
		if !CanTransition(path[i], path[i+1]) {
			t.Fatalf("expected %s -> %s", path[i], path[i+1])
		}
	}
	if !IsTerminal(Succeeded) || IsTerminal(Failed) {
		t.Fatal("terminal classification is incorrect")
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	cases := map[int]time.Duration{-2: 2 * time.Second, 1: 2 * time.Second, 3: 8 * time.Second, 20: 64 * time.Second}
	for attempt, expected := range cases {
		if actual := RetryDelay(attempt); actual != expected {
			t.Fatalf("attempt %d: got %s want %s", attempt, actual, expected)
		}
	}
}
