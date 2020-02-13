package daemon

import (
	k8sClient "github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher/resouce-event/pod"

	"github.com/golang/glog"
)

type Daemon interface {
	// Run run listener for k8s pod events.
	Run()
}

type daemon struct {
	watcher watcher.Watcher
}

// NewDaemon initializes the need components including k8s client, subnet manager client plugins, and guid pool.
// It returns error in case of failure.
func NewDaemon() (Daemon, error) {
	glog.Info("daemon NewDaemon():")
	event := pod.NewPodEvent()
	client, err := k8sClient.NewK8sClient()

	if err != nil {
		glog.Error(err)
		return nil, err
	}

	podWatcher := watcher.NewWatcher(event, client)
	return &daemon{watcher: podWatcher}, nil
}

func (d *daemon) Run() {
	glog.Info("daemon Run():")
	d.watcher.Run()
}
