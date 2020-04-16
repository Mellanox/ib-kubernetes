package ibutils

import (
	"net"
	"strings"
)

// IsPKeyValid check if the pkey is in the valid (15bits long)
func IsPKeyValid(pkey int) bool {
	return pkey == (pkey & 0x7fff)
}

// GuidToString return string guid from HardwareAddr
func GUIDToString(guidAddr net.HardwareAddr) string {
	return strings.Replace(guidAddr.String(), ":", "", -1)
}
