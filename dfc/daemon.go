/*
 * Copyright (c) 2017, NVIDIA CORPORATION. All rights reserved.
 *
 */
package dfc

import (
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
)

// runners
const (
	xproxy      = "proxy"
	xtarget     = "target"
	xsignal     = "signal"
	xproxystats = "proxystats"
	xstorstats  = "storstats"
)

//====================
//
// global
//
//====================
var ctx = &daemon{}

//=========================================
//
// types: JSON-formatted control structures
//
//=========================================
type ActionMsg struct {
	Action string `json:"action"` // shutdown, restart - see the const below
	Param1 string `json:"param1"` // action-specific params
	Param2 string `json:"param2"`
}

// ActionMsg.Action enum
const (
	ActionShutdown = "shutdown"
	ActionSyncSmap = "syncsmap" // synchronize cluster map aka Smap across all targets
)

type GetMsg struct {
	What   string `json:"what"` // specifies what exactly are we getting
	Param1 string `json:"param1"`
	Param2 string `json:"param2"`
}

// GetMsg.What enum
const (
	GetConfig = "config"
	GetStats  = "stats"
)

type CopyMsg struct {
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
}

//======
//
// types
//
//======
// FIXME: consider sync.Map; NOTE: atomic version is used by readers
type Smap struct {
	Smap    map[string]*ServerInfo `json:"smap"`
	Version int64                  `json:"version"`
	lock    *sync.Mutex
}

// daemon instance: proxy or storage target
type daemon struct {
	smap       *Smap
	config     dfconfig
	mountpaths map[string]mountPath
	rg         *rungroup
}

// each daemon is represented by:
type ServerInfo struct {
	NodeIPAddr string `json:"node_ip_addr"`
	DaemonPort string `json:"daemon_port"`
	DaemonID   string `json:"daemon_id"`
	DirectURL  string `json:"direct_url"`
}

// runner if
type runner interface {
	run() error
	stop(error)
	setname(string)
}

type namedrunner struct {
	name string
}

func (r *namedrunner) run() error       { assert(false); return nil }
func (r *namedrunner) stop(error)       { assert(false) }
func (r *namedrunner) setname(n string) { r.name = n }

// rungroup
type rungroup struct {
	runarr []runner
	runmap map[string]runner // redundant, named
	errch  chan error
	idxch  chan int
	stpch  chan error
}

type gstopError struct {
}

//====================
//
// smap wrapper with prelim atomic versioning
//
//====================
func (m *Smap) add(si *ServerInfo) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.Smap[si.DaemonID] = si
	m.Version++
}

func (m *Smap) del(sid string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	delete(m.Smap, sid)
	m.Version++
}

func (m *Smap) get(sid string) *ServerInfo {
	si, _ := m.Smap[sid]
	return si
}

func (m *Smap) count() int {
	return len(m.Smap)
}

func (m *Smap) version() int64 {
	return atomic.LoadInt64(&m.Version)
}

//====================
//
// rungroup
//
//====================
func (g *rungroup) add(r runner, name string) {
	r.setname(name)
	g.runarr = append(g.runarr, r)
	g.runmap[name] = r
}

func (g *rungroup) run() error {
	if len(g.runarr) == 0 {
		return nil
	}
	g.errch = make(chan error, len(g.runarr))
	g.idxch = make(chan int, len(g.runarr))
	g.stpch = make(chan error, 1)
	for i, r := range g.runarr {
		go func(i int, r runner) {
			g.errch <- r.run()
			g.idxch <- i
		}(i, r)
	}
	// wait here
	err := <-g.errch
	idx := <-g.idxch

	for _, r := range g.runarr {
		r.stop(err)
	}
	glog.Flush()
	for i := 0; i < cap(g.errch); i++ {
		if i == idx {
			continue
		}
		<-g.errch
		glog.Flush()
	}
	g.stpch <- nil
	return err
}

func (g *rungroup) stop() {
	g.errch <- &gstopError{}
	g.idxch <- -1
	<-g.stpch
}

func (ge *gstopError) Error() string {
	return "rungroup stop"
}

//==================
//
// daemon init & run
//
//==================
func dfcinit() {
	// CLI to override dfc JSON config
	var (
		role      string
		conffile  string
		loglevel  string
		statstime time.Duration
	)
	flag.StringVar(&role, "role", "", "role: proxy OR target")
	flag.StringVar(&conffile, "config", "", "config filename")
	flag.StringVar(&loglevel, "loglevel", "", "glog loglevel")
	flag.DurationVar(&statstime, "statstime", 0, "http and capacity utilization statistics log interval")

	flag.Parse()
	if conffile == "" {
		fmt.Fprintf(os.Stderr, "Usage: go run dfc.go -role=<proxy|target> -config=<json> [...]\n")
		os.Exit(2)
	}
	assert(role == xproxy || role == xtarget, "Invalid flag: role="+role)
	err := initconfigparam(conffile, loglevel, role, statstime)
	if err != nil {
		glog.Fatalf("Failed to initialize, config %q, err: %v", conffile, err)
	}
	assert(role == xproxy || role == xtarget, "Invalid configuration: role="+role)

	// init daemon
	ctx.rg = &rungroup{
		runarr: make([]runner, 0, 4),
		runmap: make(map[string]runner),
	}
	if role == xproxy {
		ctx.smap = &Smap{Smap: make(map[string]*ServerInfo, 8), lock: &sync.Mutex{}}
		ctx.rg.add(&proxyrunner{}, xproxy)
		ctx.rg.add(&proxystatsrunner{}, xproxystats)
	} else {
		ctx.rg.add(&targetrunner{}, xtarget)
		ctx.rg.add(&storstatsrunner{}, xstorstats)
	}
	ctx.rg.add(&sigrunner{}, xsignal)
}

// main
func Run() {
	dfcinit()
	var ok bool

	err := ctx.rg.run()
	if err == nil {
		goto m
	}
	_, ok = err.(*signalError)
	if ok {
		goto m
	}
	// dump stack trace and exit
	glog.Fatalf("============== Terminated with err: %v\n", err)
m:
	glog.Infoln("============== Terminated OK")
	glog.Flush()
}

//==================
//
// global helpers
//
//==================
func getproxystatsrunner() *proxystatsrunner {
	r := ctx.rg.runmap[xproxystats]
	rr, ok := r.(*proxystatsrunner)
	assert(ok)
	return rr
}

func getproxystats() *Proxystats {
	rr := getproxystatsrunner()
	return &rr.stats
}

func getproxy() *proxyrunner {
	r := ctx.rg.runmap[xproxy]
	rr, ok := r.(*proxyrunner)
	assert(ok)
	return rr
}

func getstorstatsrunner() *storstatsrunner {
	r := ctx.rg.runmap[xstorstats]
	rr, ok := r.(*storstatsrunner)
	assert(ok)
	return rr
}

func getstorstats() *Storstats {
	rr := getstorstatsrunner()
	return &rr.stats
}

func initusedstats() {
	r := ctx.rg.runmap[xstorstats]
	rr, ok := r.(*storstatsrunner)
	assert(ok)
	rr.used = make(map[string]int, len(ctx.mountpaths))
	for path, _ := range ctx.mountpaths {
		rr.used[path] = 0
	}
}

func getusedstats() usedstats {
	r := ctx.rg.runmap[xstorstats]
	rr, ok := r.(*storstatsrunner)
	assert(ok)
	return rr.used
}

func getcloudif() cinterface {
	r := ctx.rg.runmap[xtarget]
	rr, ok := r.(*targetrunner)
	assert(ok)
	return rr.cloudif
}
