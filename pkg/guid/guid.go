package guid

import (
	"fmt"
	"net"
)

// GUID address is an uint64 encapsulation for network hardware address
type GUID uint64

const guidLength = 8

// ParseGUID parses string only as GUID 64 bit
func ParseGUID(s string) (GUID, error) {
	ha, err := net.ParseMAC(s)
	if err != nil {
		return 0, err
	}
	if len(ha) != guidLength {
		return 0, fmt.Errorf("invalid GUID address %s", s)
	}

	return GUID(uint64(ha[0])<<56 | uint64(ha[1])<<48 | uint64(ha[2])<<40 | uint64(ha[3])<<32 | uint64(ha[4])<<24 |
		uint64(ha[5])<<16 | uint64(ha[6])<<8 | uint64(ha[7])), nil
}

// String returns the string representation of GUID
func (g GUID) String() string {
	return g.HardWareAddress().String()
}

func (g GUID) HardWareAddress() net.HardwareAddr {
	value := uint64(g)
	ha := make(net.HardwareAddr, guidLength)
	for idx := 7; idx >= 0; idx-- {
		ha[idx] = byte(value & 0xFF)
		value >>= 8
	}

	return ha
}
