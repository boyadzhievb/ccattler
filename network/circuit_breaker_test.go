package network

import (
	"sync"
	"testing"
	"time"
)

func TestCircuitBreakerAllowsRequestsWhenClosed(t *testing.T) {
	breaker := NewCircuitBreaker()

	if !breaker.AllowRequest("backend-1:8080") {
		t.Error("expected closed circuit to allow requests")
	}
}

func TestCircuitBreakerOpensAfterFailureThreshold(t *testing.T) {
	breaker := NewCircuitBreaker()
	backendAddress := "backend-1:8080"

	for failureIndex := 0; failureIndex < breaker.config.failureThreshold; failureIndex++ {
		breaker.RecordFailure(backendAddress)
	}

	if breaker.AllowRequest(backendAddress) {
		t.Error("expected open circuit to reject requests after threshold failures")
	}

	if breaker.State(backendAddress) != CircuitOpen {
		t.Errorf("expected state CircuitOpen, got %d", breaker.State(backendAddress))
	}
}

func TestCircuitBreakerSuccessResetsFailureCount(t *testing.T) {
	breaker := NewCircuitBreaker()
	backendAddress := "backend-1:8080"

	for failureIndex := 0; failureIndex < breaker.config.failureThreshold-1; failureIndex++ {
		breaker.RecordFailure(backendAddress)
	}

	breaker.RecordSuccess(backendAddress)

	breaker.RecordFailure(backendAddress)

	if !breaker.AllowRequest(backendAddress) {
		t.Error("expected circuit to remain closed after success reset")
	}
}

func TestCircuitBreakerTransitionsToHalfOpenAfterDuration(t *testing.T) {
	currentTime := time.Now()
	breaker := NewCircuitBreaker()
	breaker.timeNow = func() time.Time { return currentTime }
	backendAddress := "backend-1:8080"

	for failureIndex := 0; failureIndex < breaker.config.failureThreshold; failureIndex++ {
		breaker.RecordFailure(backendAddress)
	}

	if breaker.AllowRequest(backendAddress) {
		t.Error("expected open circuit to reject requests")
	}

	currentTime = currentTime.Add(breaker.config.openDuration + time.Second)

	if !breaker.AllowRequest(backendAddress) {
		t.Error("expected half-open circuit to allow a probe request")
	}

	if breaker.State(backendAddress) != CircuitHalfOpen {
		t.Errorf("expected state CircuitHalfOpen, got %d", breaker.State(backendAddress))
	}
}

func TestCircuitBreakerHalfOpenLimitsProbeRequests(t *testing.T) {
	currentTime := time.Now()
	breaker := NewCircuitBreaker()
	breaker.timeNow = func() time.Time { return currentTime }
	backendAddress := "backend-1:8080"

	for failureIndex := 0; failureIndex < breaker.config.failureThreshold; failureIndex++ {
		breaker.RecordFailure(backendAddress)
	}

	currentTime = currentTime.Add(breaker.config.openDuration + time.Second)

	if !breaker.AllowRequest(backendAddress) {
		t.Error("expected first probe request to be allowed")
	}

	if breaker.AllowRequest(backendAddress) {
		t.Error("expected second probe request to be rejected in half-open state")
	}
}

func TestCircuitBreakerHalfOpenClosesAfterSuccessThreshold(t *testing.T) {
	currentTime := time.Now()
	breaker := NewCircuitBreaker()
	breaker.timeNow = func() time.Time { return currentTime }
	backendAddress := "backend-1:8080"

	for failureIndex := 0; failureIndex < breaker.config.failureThreshold; failureIndex++ {
		breaker.RecordFailure(backendAddress)
	}

	currentTime = currentTime.Add(breaker.config.openDuration + time.Second)
	breaker.AllowRequest(backendAddress)

	for successIndex := 0; successIndex < breaker.config.successThreshold; successIndex++ {
		breaker.RecordSuccess(backendAddress)
	}

	if breaker.State(backendAddress) != CircuitClosed {
		t.Errorf("expected state CircuitClosed after success threshold, got %d", breaker.State(backendAddress))
	}

	if !breaker.AllowRequest(backendAddress) {
		t.Error("expected closed circuit to allow requests")
	}
}

func TestCircuitBreakerHalfOpenFailureReopens(t *testing.T) {
	currentTime := time.Now()
	breaker := NewCircuitBreaker()
	breaker.timeNow = func() time.Time { return currentTime }
	backendAddress := "backend-1:8080"

	for failureIndex := 0; failureIndex < breaker.config.failureThreshold; failureIndex++ {
		breaker.RecordFailure(backendAddress)
	}

	currentTime = currentTime.Add(breaker.config.openDuration + time.Second)
	breaker.AllowRequest(backendAddress)
	breaker.RecordFailure(backendAddress)

	if breaker.State(backendAddress) != CircuitOpen {
		t.Errorf("expected state CircuitOpen after half-open failure, got %d", breaker.State(backendAddress))
	}

	if breaker.AllowRequest(backendAddress) {
		t.Error("expected reopened circuit to reject requests")
	}
}

func TestCircuitBreakerIndependentPerBackend(t *testing.T) {
	breaker := NewCircuitBreaker()
	backendHealthy := "backend-healthy:8080"
	backendFailing := "backend-failing:8080"

	for failureIndex := 0; failureIndex < breaker.config.failureThreshold; failureIndex++ {
		breaker.RecordFailure(backendFailing)
	}

	if !breaker.AllowRequest(backendHealthy) {
		t.Error("expected healthy backend to allow requests")
	}

	if breaker.AllowRequest(backendFailing) {
		t.Error("expected failing backend to reject requests")
	}
}

func TestCircuitBreakerResetClearsAllState(t *testing.T) {
	breaker := NewCircuitBreaker()
	backendAddress := "backend-1:8080"

	for failureIndex := 0; failureIndex < breaker.config.failureThreshold; failureIndex++ {
		breaker.RecordFailure(backendAddress)
	}

	breaker.Reset()

	if breaker.State(backendAddress) != CircuitClosed {
		t.Errorf("expected state CircuitClosed after reset, got %d", breaker.State(backendAddress))
	}

	if !breaker.AllowRequest(backendAddress) {
		t.Error("expected requests to be allowed after reset")
	}
}

func TestCircuitBreakerConcurrentAccess(t *testing.T) {
	breaker := NewCircuitBreaker()
	backendAddress := "backend-1:8080"
	waitGroup := sync.WaitGroup{}

	for goroutineIndex := 0; goroutineIndex < 100; goroutineIndex++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			breaker.AllowRequest(backendAddress)
			if index%2 == 0 {
				breaker.RecordSuccess(backendAddress)
			} else {
				breaker.RecordFailure(backendAddress)
			}
		}(goroutineIndex)
	}

	waitGroup.Wait()
}

func TestCircuitBreakerUnknownBackendReturnsClosed(t *testing.T) {
	breaker := NewCircuitBreaker()

	state := breaker.State("never-seen:8080")
	if state != CircuitClosed {
		t.Errorf("expected CircuitClosed for unknown backend, got %d", state)
	}
}

func TestCircuitBreakerRecordSuccessOnUnknownBackendIsNoOp(t *testing.T) {
	breaker := NewCircuitBreaker()
	breaker.RecordSuccess("never-seen:8080")

	if breaker.State("never-seen:8080") != CircuitClosed {
		t.Error("expected no state created for unknown backend on success")
	}
}

func TestCircuitBreakerStateReflectsHalfOpenTransition(t *testing.T) {
	currentTime := time.Now()
	breaker := NewCircuitBreaker()
	breaker.timeNow = func() time.Time { return currentTime }
	backendAddress := "backend-1:8080"

	for failureIndex := 0; failureIndex < breaker.config.failureThreshold; failureIndex++ {
		breaker.RecordFailure(backendAddress)
	}

	if breaker.State(backendAddress) != CircuitOpen {
		t.Errorf("expected CircuitOpen, got %d", breaker.State(backendAddress))
	}

	currentTime = currentTime.Add(breaker.config.openDuration + time.Second)

	if breaker.State(backendAddress) != CircuitHalfOpen {
		t.Errorf("expected CircuitHalfOpen after open duration, got %d", breaker.State(backendAddress))
	}
}
