package plugins

type SubnetManagerClient interface {
	// Name returns the subnet manager name.
	Name() string

	// Connect Check the client can reach the subnet manager and return error in case if it is not reachable.
	Validate() error

	// AddPKey add pkey for the given guid.
	// It return error if failed to add the pkey for specified guid.
	AddPKey(pkey, guid string) error

	// RemovePKey remove pkey for the given guid.
	// It return error if failed to remove the pkey for specified guid.
	RemovePKey(pkey, guid string) error
}
