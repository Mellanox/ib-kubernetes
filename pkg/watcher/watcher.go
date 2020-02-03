package watcher

import (
	resEvent "github.com/Mellanox/ib-kubernetes/pkg/watcher/resouce-event"
)

type Watcher interface {
	Run()
}

type watcher struct {
}

func NewWatcher(event resEvent.ResourceEvent) Watcher {
	return &watcher{}
}

func (p *watcher) Run() {
}
