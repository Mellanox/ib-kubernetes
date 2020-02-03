package guid

type GuidPool interface {
	// InitPool initializes the pool and marks the allocated guids from previous session.
	// It returns error in case of failure.
	InitPool() error

	// AllocateGUID allocate the next free guid in the range.
	// It returns the allocated guid or error if range is full.
	AllocateGUID() (string, error)

	// ReleaseGUID release the reservation of the guid.
	// It returns error if the guid is not in the range.
	ReleaseGUID(string) error
}

type guidPool struct {
}

func NewGuidPool(guidRangeStart, guidRangeEnd string) GuidPool {
	return &guidPool{}
}

func (p *guidPool) InitPool() error {
	return nil
}

func (p *guidPool) AllocateGUID() (string, error) {
	return "", nil
}

func (p *guidPool) ReleaseGUID(guid string) error {
	return nil
}
