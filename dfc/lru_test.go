/*
 * Copyright (c) 2017, NVIDIA CORPORATION. All rights reserved.
 *
 */

// Evict the specified directory to its current (filesystem) utilization - 5%
// as in:
// 	low-watermark = current-usage - 5%
//
// Example run:
// 	go test -v -run=lru -args -dir /tmp/eviction
//
package dfc

import (
	"flag"
	"sync"
	"syscall"
	"testing"
)

var dir string

func init() {
	flag.StringVar(&dir, "dir", "/tmp/eviction", "directory to evict")
	flag.Lookup("log_dir").Value.Set("/tmp")
	flag.Lookup("v").Value.Set("4")
}

// e.g. run: go test -v -run=lru -args -dir /tmp/eviction
func Test_lru(t *testing.T) {
	flag.Parse()

	statfs := syscall.Statfs_t{}
	if err := syscall.Statfs(dir, &statfs); err != nil {
		t.Logf("Failed to statfs %q, err: %v", dir, err)
		return
	}
	usedpct := (statfs.Blocks - statfs.Bavail) * 100 / statfs.Blocks
	if usedpct < 10 {
		t.Log("Nothing to do", "dir", dir, "used", usedpct)
		return
	}
	lwm := usedpct - 5
	hwm := usedpct - 1
	t.Logf("Pre-eviction:  used %d%%, lwm %d%%, hwm %d%%", usedpct, lwm, hwm)

	ctx.config.Cache.FSHighWaterMark = uint32(hwm)
	ctx.config.Cache.FSLowWaterMark = uint32(lwm)

	fschkwg := &sync.WaitGroup{}
	fschkwg.Add(1)
	one_LRU(dir, fschkwg)

	// check results
	statfs = syscall.Statfs_t{}
	if err := syscall.Statfs(dir, &statfs); err != nil {
		t.Errorf("Failed to statfs %q, err: %v", dir, err)
		return
	}
	usedpct = (statfs.Blocks - statfs.Bavail) * 100 / statfs.Blocks
	if usedpct < lwm-1 || usedpct > lwm+1 {
		t.Errorf("Failed to reach lwm %d%%, post eviction used %d%%", lwm, usedpct)
	} else {
		t.Logf("Post-eviction: used %d%%, lwm %d%%, hwm %d%%", usedpct, lwm, hwm)
	}
}
