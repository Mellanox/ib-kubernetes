package errcode

import "fmt"

type errCode struct {
	code int
	text string
}

const (
	// Value for destinguishing non-errCode type.
	// Not used by errCode itself.
	NotErrCodeType = iota - 1
)

func (e *errCode) Error() string {
	return e.text
}

// Return error code value or NotErrCodeType if variable isn't of type errCode
func GetCode(e error) int {
	err, ok := e.(*errCode)
	if !ok {
		return NotErrCodeType
	}
	return err.code
}

// Create new errCode
func NewErr(code int, text string) error {
	return &errCode{code: code, text: text}
}

// Create new errCode with formated text.
func Errorf(code int, format string, a ...interface{}) error {
	return &errCode{code: code, text: fmt.Sprintf(format, a...)}
}
