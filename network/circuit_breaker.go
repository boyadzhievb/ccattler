package network

import (
	"sync"
	"time"
)

// CircuitState represents the current state of a circuit breaker.
type CircuitState int

const (
	// CircuitClosed means the backend is healthy and requests flow normally.
	CircuitClosed CircuitState = iota
	// CircuitOpen means the backend is unhealthy and requests are rejected.
	CircuitOpen
	// CircuitHalfOpen means the breaker is probing to check if the backend recovered.
	CircuitHalfOpen
)

// circuitBreakerConfig holds the thresholds for a circuit breaker.
type circuitBreakerConfig struct {
	failureThreshold    int
	successThreshold    int
	openDuration        time.Duration
	halfOpenMaxRequests int
}

// defaultCircuitBreakerConfig returns sensible defaults for proxy backends.
func defaultCircuitBreakerConfig() circuitBreakerConfig {
	return circuitBreakerConfig{
		failureThreshold:    5,
		successThreshold:    2,
		openDuration:        10 * time.Second,
		halfOpenMaxRequests: 1,
	}
}

// backendCircuit tracks the circuit breaker state for a single backend endpoint.
type backendCircuit struct {
	state              CircuitState
	consecutiveFailures int
	consecutiveSuccesses int
	lastFailureTime    time.Time
	halfOpenInFlight   int
}

// CircuitBreaker tracks per-backend health and prevents forwarding requests
// to backends that are consistently failing. It uses the standard three-state
// model: closed (healthy), open (unhealthy, requests rejected), half-open
// (probing for recovery).
type CircuitBreaker struct {
	config   circuitBreakerConfig
	circuits map[string]*backendCircuit
	mutex    sync.Mutex
	timeNow  func() time.Time
}

// NewCircuitBreaker creates a circuit breaker with default configuration.
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		config:   defaultCircuitBreakerConfig(),
		circuits: make(map[string]*backendCircuit),
		timeNow:  time.Now,
	}
}

// AllowRequest checks whether a request to the given backend should proceed.
// Returns true if the circuit is closed or half-open (probe slot available).
func (circuitBreaker *CircuitBreaker) AllowRequest(backendAddress string) bool {
	circuitBreaker.mutex.Lock()
	defer circuitBreaker.mutex.Unlock()

	circuit, exists := circuitBreaker.circuits[backendAddress]
	if !exists {
		return true
	}

	switch circuit.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if circuitBreaker.timeNow().Sub(circuit.lastFailureTime) >= circuitBreaker.config.openDuration {
			circuit.state = CircuitHalfOpen
			circuit.halfOpenInFlight = 1
			circuit.consecutiveSuccesses = 0
			return true
		}
		return false
	case CircuitHalfOpen:
		if circuit.halfOpenInFlight < circuitBreaker.config.halfOpenMaxRequests {
			circuit.halfOpenInFlight++
			return true
		}
		return false
	}
	return true
}

// RecordSuccess marks a successful response from the backend. In half-open
// state, enough successes close the circuit. In closed state, resets the
// failure counter.
func (circuitBreaker *CircuitBreaker) RecordSuccess(backendAddress string) {
	circuitBreaker.mutex.Lock()
	defer circuitBreaker.mutex.Unlock()

	circuit, exists := circuitBreaker.circuits[backendAddress]
	if !exists {
		return
	}

	circuit.consecutiveFailures = 0

	switch circuit.state {
	case CircuitHalfOpen:
		circuit.consecutiveSuccesses++
		circuit.halfOpenInFlight--
		if circuit.consecutiveSuccesses >= circuitBreaker.config.successThreshold {
			circuit.state = CircuitClosed
			circuit.consecutiveSuccesses = 0
		}
	case CircuitClosed:
		circuit.consecutiveSuccesses = 0
	}
}

// RecordFailure marks a failed response from the backend. After enough
// consecutive failures, the circuit opens and rejects further requests
// until the open duration elapses.
func (circuitBreaker *CircuitBreaker) RecordFailure(backendAddress string) {
	circuitBreaker.mutex.Lock()
	defer circuitBreaker.mutex.Unlock()

	circuit, exists := circuitBreaker.circuits[backendAddress]
	if !exists {
		circuit = &backendCircuit{state: CircuitClosed}
		circuitBreaker.circuits[backendAddress] = circuit
	}

	circuit.consecutiveSuccesses = 0
	circuit.consecutiveFailures++
	circuit.lastFailureTime = circuitBreaker.timeNow()

	switch circuit.state {
	case CircuitClosed:
		if circuit.consecutiveFailures >= circuitBreaker.config.failureThreshold {
			circuit.state = CircuitOpen
		}
	case CircuitHalfOpen:
		circuit.halfOpenInFlight--
		circuit.state = CircuitOpen
	}
}

// State returns the current circuit state for a backend. Returns CircuitClosed
// for unknown backends.
func (circuitBreaker *CircuitBreaker) State(backendAddress string) CircuitState {
	circuitBreaker.mutex.Lock()
	defer circuitBreaker.mutex.Unlock()

	circuit, exists := circuitBreaker.circuits[backendAddress]
	if !exists {
		return CircuitClosed
	}

	if circuit.state == CircuitOpen && circuitBreaker.timeNow().Sub(circuit.lastFailureTime) >= circuitBreaker.config.openDuration {
		return CircuitHalfOpen
	}
	return circuit.state
}

// Reset clears all circuit breaker state, returning all backends to closed.
func (circuitBreaker *CircuitBreaker) Reset() {
	circuitBreaker.mutex.Lock()
	defer circuitBreaker.mutex.Unlock()
	circuitBreaker.circuits = make(map[string]*backendCircuit)
}
