package ib_utils

import (
	"net"
	"strings"
)

// IsPKeyValid check if the pkey is in the valid range
func IsPKeyValid(pkey int) bool {
	return pkey > 0x0000 && pkey < 0xFFFF
}

// GuidToString return string guid from HardwareAddr
func GuidToString(guidAddr net.HardwareAddr) string {
	return strings.Replace(guidAddr.String(), ":", "", -1)
}
