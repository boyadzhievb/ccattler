package types

import "fmt"

// ErrorCode classifies the kind of error for programmatic handling. API handlers
// translate these codes to HTTP status codes; CLI consumers use them for retry
// decisions and user-facing messages.
type ErrorCode string

const (
	ErrorCodeNotFound      ErrorCode = "not_found"
	ErrorCodeAlreadyExists ErrorCode = "already_exists"
	ErrorCodeConflict      ErrorCode = "conflict"
	ErrorCodeInvalidInput  ErrorCode = "invalid_input"
	ErrorCodeUnauthorized  ErrorCode = "unauthorized"
	ErrorCodeForbidden     ErrorCode = "forbidden"
	ErrorCodeQuotaExceeded ErrorCode = "quota_exceeded"
	ErrorCodeInternal      ErrorCode = "internal"
	ErrorCodeUnavailable   ErrorCode = "unavailable"
)

// CCattlerError is the structured error type for all domain errors. It carries a
// machine-readable code, a human-readable message, and an optional wrapped cause.
type CCattlerError struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (ccattlerError *CCattlerError) Error() string {
	if ccattlerError.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", ccattlerError.Code, ccattlerError.Message, ccattlerError.Cause)
	}
	return fmt.Sprintf("%s: %s", ccattlerError.Code, ccattlerError.Message)
}

func (ccattlerError *CCattlerError) Unwrap() error {
	return ccattlerError.Cause
}

func NewNotFoundError(resourceType string, resourceName string) *CCattlerError {
	return &CCattlerError{
		Code:    ErrorCodeNotFound,
		Message: fmt.Sprintf("%s %q not found", resourceType, resourceName),
	}
}

func NewAlreadyExistsError(resourceType string, resourceName string) *CCattlerError {
	return &CCattlerError{
		Code:    ErrorCodeAlreadyExists,
		Message: fmt.Sprintf("%s %q already exists", resourceType, resourceName),
	}
}

func NewConflictError(message string, cause error) *CCattlerError {
	return &CCattlerError{
		Code:    ErrorCodeConflict,
		Message: message,
		Cause:   cause,
	}
}

func NewInvalidInputError(message string) *CCattlerError {
	return &CCattlerError{
		Code:    ErrorCodeInvalidInput,
		Message: message,
	}
}

func NewUnauthorizedError(message string) *CCattlerError {
	return &CCattlerError{
		Code:    ErrorCodeUnauthorized,
		Message: message,
	}
}

func NewForbiddenError(principal string, action string, target string) *CCattlerError {
	return &CCattlerError{
		Code:    ErrorCodeForbidden,
		Message: fmt.Sprintf("%s is not allowed to %s %s", principal, action, target),
	}
}

func NewQuotaExceededError(tenantName string, resource string) *CCattlerError {
	return &CCattlerError{
		Code:    ErrorCodeQuotaExceeded,
		Message: fmt.Sprintf("tenant %q exceeded %s quota", tenantName, resource),
	}
}

func NewInternalError(message string, cause error) *CCattlerError {
	return &CCattlerError{
		Code:    ErrorCodeInternal,
		Message: message,
		Cause:   cause,
	}
}

func NewUnavailableError(message string) *CCattlerError {
	return &CCattlerError{
		Code:    ErrorCodeUnavailable,
		Message: message,
	}
}

// IsErrorCode checks whether an error has the given CCattlerError code.
func IsErrorCode(err error, code ErrorCode) bool {
	ccattlerError, isTyped := err.(*CCattlerError)
	return isTyped && ccattlerError.Code == code
}
