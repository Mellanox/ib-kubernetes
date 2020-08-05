// Package errcode defines the errCode type, which extend common error handling,
// by providing error code value in addition to error message.
//
// To start using a package, at first You need to implement desired error codes.
// Example:
//   const (
//        ErrorUnknown = iota // NOTE: should start from 0
//        ErrorFirst
//        ...
//        ErrorLast
//   )
//
// To create new errCode with formatted text use `Errorf' method. Example:
//   err := errcode.Errorf(ErrorFirst, "Some text describing error. Reason: %s", reason)
//
// To get error code value use `GetCode' method, text - `Error' method. Example:
//   if errcode.GetCode(err) == ErrorUnknown {
//        <do something>
//        fmt.Println(err.Error())
//   }
//
// For code examples refer to:
// https://github.com/Mellanox/ib-kubernetes/blob/master/pkg/daemon/daemon.go
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

// Error returns error message.
func (e *errCode) Error() string {
	return e.text
}

// GetCode returns error code value or NotErrCodeType if variable isn't of type errCode.
func GetCode(e error) int {
	err, ok := e.(*errCode)
	if !ok {
		return NotErrCodeType
	}
	return err.code
}

// Errorf creates new errCode with formated text.
func Errorf(code int, format string, a ...interface{}) error {
	return &errCode{code: code, text: fmt.Sprintf(format, a...)}
}
