package daemon

import (
	"github.com/Mellanox/ib-kubernetes/pkg/types"
)

type daemon struct {
}

func NewDaemon() types.Daemon {
	return &daemon{}
}
