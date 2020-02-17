package plugins

import "net"

type SubnetManagerClient interface {
	// Name returns the name of the plugin
	Name() string

	// SpecVersion returns the version of the spec of the plugin
	Spec() string

	// Validate Check the client can reach the subnet manager and return error in case if it is not reachable.
	Validate() error

	// AddGuidsToPKey add pkey for the given guid.
	// It return error if failed.
	AddGuidsToPKey(pkey int, guids []net.HardwareAddr) error

	// RemoveGuidsFromPKey remove guids for given pkey.
	// It return error if failed.
	RemoveGuidsFromPKey(pkey int, guids []net.HardwareAddr) error
}
