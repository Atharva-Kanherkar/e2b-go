package e2b

import (
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
)

// Sentinel errors returned by this package. Callers should branch on them
// with errors.Is.
var (
	// ErrSandboxNotFound indicates the sandbox ID is unknown to the control
	// plane — typically because it has already been destroyed or timed out.
	ErrSandboxNotFound = errors.New("e2b: sandbox not found")

	// ErrFileNotFound is returned by file operations when the target path
	// does not exist in the sandbox.
	ErrFileNotFound = errors.New("e2b: file not found")

	// ErrSandboxDestroyed is returned by any operation on a Sandbox whose
	// Destroy has already been called.
	ErrSandboxDestroyed = errors.New("e2b: sandbox is destroyed")
)

func normalizeHTTPError(statusCode int, body string, notFoundErr error) error {
	switch statusCode {
	case http.StatusNotFound:
		if notFoundErr != nil {
			return notFoundErr
		}
		return fmt.Errorf("e2b: resource not found: %s", body)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("e2b: authentication failed: %s", body)
	default:
		return fmt.Errorf("e2b: request failed with status %d: %s", statusCode, body)
	}
}

func normalizeRPCError(err error) error {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return err
	}
	switch connectErr.Code() {
	case connect.CodeNotFound:
		return ErrFileNotFound
	default:
		return fmt.Errorf("e2b: rpc failed: %w", err)
	}
}
