package errcode

const (
	// Value for destinguishing non-errCode type.
	// Not used by errCode.
	NotErrCodeType = iota - 1

	ErrUnknown // Default value
	ErrGUIDAlreadyAllocated
	ErrNetworkNotConfigured
	ErrNotIBSriovNetwork

	// Number of error codes.
	// To implement new ones define them with `iota + ErrMax + 1' at first line in const definition block
	// Keep last.
	ErrMax
)
