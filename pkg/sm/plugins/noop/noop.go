package main

import (
	"github.com/golang/glog"
	"log"
)

var Plugin plugin
var InvalidPlugin bool

func init() {
	Plugin = plugin{}
	glog.Infof("noop Plugin: %v %v", Plugin, InvalidPlugin)
}

type plugin struct {
}

func (p *plugin) Name() string {
	return "noop"
}

func (p *plugin) Validate() error {
	log.Println("noop Plugin Validate()")
	return nil
}

func (p *plugin) AddPKey(pkey, guid string) error {
	log.Println("noop Plugin AddPkey()")
	return nil
}

func (p *plugin) RemovePKey(pkey, guid string) error {
	log.Println("noop Plugin RemovePKey()")
	return nil
}
