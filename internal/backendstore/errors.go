package backendstore

import "fmt"

// APIError exposes stable status/code without echoing remote bodies or credentials.
type APIError struct {
	StatusCode         int
	Code, Method, Path string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("backend %s %s returned HTTP %d (%s)", e.Method, e.Path, e.StatusCode, e.Code)
}
func statusCode(status int) string {
	switch status {
	case 400:
		return "validation_error"
	case 401:
		return "unauthorized"
	case 403:
		return "forbidden"
	case 404:
		return "not_found"
	case 409:
		return "conflict"
	case 412:
		return "stale_revision"
	case 428:
		return "precondition_required"
	case 503:
		return "store_error"
	default:
		return "unexpected_response"
	}
}

// UncertainWriteError means a keyed request may have committed. The caller must
// retain that key/payload for receipt recovery; never retry with a fresh key or
// report the command successful merely because the request was sent.
type UncertainWriteError struct {
	Key   string
	Cause error
}

func (e *UncertainWriteError) Error() string {
	return fmt.Sprintf("backend write outcome is uncertain; retain operation key %s for recovery: %v", e.Key, e.Cause)
}
func (e *UncertainWriteError) Unwrap() error { return e.Cause }
func requestFailure(method, key string, cause error) error {
	if method != "GET" && method != "HEAD" && key != "" {
		return &UncertainWriteError{Key: key, Cause: cause}
	}
	return cause
}
