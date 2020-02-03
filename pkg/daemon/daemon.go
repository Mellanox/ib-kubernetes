package daemon

type Daemon interface {
	// Init initializes the need components including k8s client, subnet manager client plugins, and guid pool.
	// It returns error in case of failure.
	Init() error

	// Run run listener for k8s pod events.
	// It returns error in case of failure.
	Run() error
}

type daemon struct {
}

func NewDaemon() Daemon {
	return &daemon{}
}

func (d *daemon) Init() error {
	return nil
}

func (d *daemon) Run() error {
	return nil
}
