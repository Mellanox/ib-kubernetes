// Copyright 2025 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

// Package errcode defines the errCode type, which extend common error handling,
// by providing error code value in addition to error message.
//
// To start using a package, at first You need to implement desired error codes.
// Example:
//
//	const (
//	     ErrorUnknown = iota // NOTE: should start from 0
//	     ErrorFirst
//	     ...
//	     ErrorLast
//	)
//
// To create new errCode with formatted text use `Errorf' method. Example:
//
//	err := errcode.Errorf(ErrorFirst, "Some text describing error. Reason: %s", reason)
//
// To get error code value use `GetCode' method, text - `Error' method. Example:
//
//	if errcode.GetCode(err) == ErrorUnknown {
//	     <do something>
//	     fmt.Println(err.Error())
//	}
//
// For code examples refer to:
// https://github.com/Mellanox/ib-kubernetes/blob/master/pkg/daemon/daemon.go
package errcode

import "fmt"

//nolint:errname
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
