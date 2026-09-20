package types

import (
	"errors"
	"fmt"
	"testing"
)

func TestNotFoundErrorMessage(t *testing.T) {
	notFoundError := NewNotFoundError("service", "web")
	expected := `not_found: service "web" not found`
	if notFoundError.Error() != expected {
		t.Errorf("got %q, want %q", notFoundError.Error(), expected)
	}
	if notFoundError.Code != ErrorCodeNotFound {
		t.Errorf("code: got %q, want %q", notFoundError.Code, ErrorCodeNotFound)
	}
}

func TestAlreadyExistsErrorMessage(t *testing.T) {
	existsError := NewAlreadyExistsError("tenant", "payments")
	if existsError.Code != ErrorCodeAlreadyExists {
		t.Errorf("code: got %q, want %q", existsError.Code, ErrorCodeAlreadyExists)
	}
	if existsError.Unwrap() != nil {
		t.Error("expected nil cause for already-exists error")
	}
}

func TestConflictErrorWraps(t *testing.T) {
	causeError := fmt.Errorf("revision mismatch")
	conflictError := NewConflictError("optimistic lock failed", causeError)
	if !errors.Is(conflictError, causeError) {
		t.Error("expected conflict error to wrap the cause")
	}
	if conflictError.Code != ErrorCodeConflict {
		t.Errorf("code: got %q, want %q", conflictError.Code, ErrorCodeConflict)
	}
}

func TestInternalErrorWithCause(t *testing.T) {
	causeError := fmt.Errorf("connection refused")
	internalError := NewInternalError("store failure", causeError)
	if !errors.Is(internalError, causeError) {
		t.Error("expected internal error to wrap the cause")
	}
	expectedMessage := "internal: store failure: connection refused"
	if internalError.Error() != expectedMessage {
		t.Errorf("got %q, want %q", internalError.Error(), expectedMessage)
	}
}

func TestForbiddenErrorMessage(t *testing.T) {
	forbiddenError := NewForbiddenError("node:n1", "write", "desired/service/web")
	if forbiddenError.Code != ErrorCodeForbidden {
		t.Errorf("code: got %q, want %q", forbiddenError.Code, ErrorCodeForbidden)
	}
}

func TestQuotaExceededError(t *testing.T) {
	quotaError := NewQuotaExceededError("payments", "instances")
	if quotaError.Code != ErrorCodeQuotaExceeded {
		t.Errorf("code: got %q, want %q", quotaError.Code, ErrorCodeQuotaExceeded)
	}
}

func TestIsErrorCode(t *testing.T) {
	notFoundError := NewNotFoundError("node", "node-1")
	if !IsErrorCode(notFoundError, ErrorCodeNotFound) {
		t.Error("expected IsErrorCode to match not_found")
	}
	if IsErrorCode(notFoundError, ErrorCodeConflict) {
		t.Error("expected IsErrorCode to not match conflict")
	}
	if IsErrorCode(fmt.Errorf("plain error"), ErrorCodeNotFound) {
		t.Error("expected IsErrorCode to return false for non-CCattlerError")
	}
}

func TestErrorsAsTyped(t *testing.T) {
	notFoundError := NewNotFoundError("instance", "inst-abc")
	var ccattlerError *CCattlerError
	if !errors.As(notFoundError, &ccattlerError) {
		t.Error("expected errors.As to find CCattlerError")
	}
	if ccattlerError.Code != ErrorCodeNotFound {
		t.Errorf("code via As: got %q, want %q", ccattlerError.Code, ErrorCodeNotFound)
	}
}

func TestUnavailableError(t *testing.T) {
	unavailableError := NewUnavailableError("etcd cluster unreachable")
	if unavailableError.Code != ErrorCodeUnavailable {
		t.Errorf("code: got %q, want %q", unavailableError.Code, ErrorCodeUnavailable)
	}
}

func TestInvalidInputError(t *testing.T) {
	inputError := NewInvalidInputError("port must be between 1 and 65535")
	if inputError.Code != ErrorCodeInvalidInput {
		t.Errorf("code: got %q, want %q", inputError.Code, ErrorCodeInvalidInput)
	}
}

func TestUnauthorizedError(t *testing.T) {
	authError := NewUnauthorizedError("missing client certificate")
	if authError.Code != ErrorCodeUnauthorized {
		t.Errorf("code: got %q, want %q", authError.Code, ErrorCodeUnauthorized)
	}
}
