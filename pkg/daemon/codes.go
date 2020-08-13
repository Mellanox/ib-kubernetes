package daemon

const (
	ErrUnknown = iota // Default value
	ErrGUIDAlreadyAllocated
	ErrNetworkNotConfigured
	ErrNotIBSriovNetwork
	ErrGetNetAttachDef
)
