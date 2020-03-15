package main

import (
	"flag"
	"os"

	"github.com/golang/glog"

	"github.com/Mellanox/ib-kubernetes/pkg/daemon"
)

func main() {
	flag.Parse()
	glog.Info("main(): Starting InfiniBand Daemon")
	ibDaemon, err := daemon.NewDaemon()
	if err != nil {
		glog.Errorf("main(): daemon failed to create daemon: %v", err)
		os.Exit(1)
	}

	glog.Info("main(): Running InfiniBand Daemon")
	ibDaemon.Run()
}
