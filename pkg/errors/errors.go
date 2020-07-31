package errors

import "fmt"

const (
	// Value for destinguishing non-codedError type.
	// Not used by codedError.
	NotCodedErrorType = iota - 1

	UnknownError // Default value
	ExistError
	NotValidError

	// Number of error codes. Add new one before it.
	// Keep last.
	ErrorMax
)

type codedError struct {
	code int
	text string
}

func (e codedError) Error() string {
	return e.text
}

// Return error code value or NotCodedErrorType if variable isn't of type codedError
func ErrorCode(e error) int {
	custom, ok := e.(*codedError)
	if !ok {
		return NotCodedErrorType
	}
	return custom.code
}

// Create new codedError.
// If code !(NotCodedErrorType < code < ErrorMax) use UnknownError.
func Errorf(code int, format string, a ...interface{}) error {
	if code <= NotCodedErrorType || code >= ErrorMax {
		code = UnknownError
	}
	return &codedError{code: code, text: fmt.Sprintf(format, a...)}
}
