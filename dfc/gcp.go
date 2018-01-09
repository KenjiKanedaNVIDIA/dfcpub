/*
 * Copyright (c) 2017, NVIDIA CORPORATION. All rights reserved.
 *
 */
package dfc

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"

	"cloud.google.com/go/storage"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/api/iterator"
)

func getProjID() (string, string) {
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		return "", "Failed to get ProjectID from GCP"
	}
	return projectID, ""
}

func (obj *gcpif) listbucket(w http.ResponseWriter, bucket string) error {
	glog.Infof("listbucket %s", bucket)
	projid, errstr := getProjID()
	if projid == "" {
		return webinterror(w, errstr)
	}
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		glog.Fatal(err)
	}
	it := client.Bucket(bucket).Objects(ctx, nil)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			errstr := fmt.Sprintf("Failed to get bucket objects, err: %v", err)
			return webinterror(w, errstr)
		}
		fmt.Fprintln(w, attrs.Name)
	}
	return nil
}

// FIXME: revisit error processing
func (obj *gcpif) getobj(w http.ResponseWriter, mpath string, bktname string, objname string) error {
	fname := mpath + "/" + bktname + "/" + objname

	projid, errstr := getProjID()
	if projid == "" {
		return webinterror(w, errstr)
	}
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		glog.Fatal(err)
	}
	o := client.Bucket(bktname).Object(objname)
	attrs, err := o.Attrs(ctx)
	if err != nil {
		errstr := fmt.Sprintf("Failed to get attributes for object %s from bucket %s, err: %v", objname, bktname, err)
		return webinterror(w, errstr)
	}
	omd5 := attrs.MD5
	rc, err := o.NewReader(ctx)
	if err != nil {
		errstr := fmt.Sprintf("Failed to create rc for object %s to file %q, err: %v", objname, fname, err)
		return webinterror(w, errstr)
	}
	defer rc.Close()
	// strips the last part from filepath
	dirname := filepath.Dir(fname)
	if err = CreateDir(dirname); err != nil {
		glog.Errorf("Failed to create local dir %q, err: %s", dirname, err)
		return webinterror(w, errstr)
	}
	file, err := os.Create(fname)
	if err != nil {
		errstr := fmt.Sprintf("Failed to create file %q, err: %v", fname, err)
		return webinterror(w, errstr)
	} else {
		glog.Infof("Created file %q", fname)
	}
	bytes, err := io.Copy(file, rc)
	if err != nil {
		errstr := fmt.Sprintf("Failed to download object %s to file %q, err: %v", objname, fname, err)
		return webinterror(w, errstr)
		// FIXME: checksetmounterror() - see aws.go
	}
	dmd5, err := computeMD5(fname)
	if err != nil {
		errstr := fmt.Sprintf("Failed to calculate MD5sum for downloaded file %s, err: %v", fname, err)
		return webinterror(w, errstr)
	}
	if reflect.DeepEqual(omd5, dmd5) == false {
		errstr := fmt.Sprintf("Object's %s MD5sum %v does not match with downloaded file MD5sum %v", objname, omd5, dmd5)
		// Remove downloaded file.
		err := os.Remove(fname)
		if err != nil {
			glog.Errorf("Failed to delete file %s, err: %v", fname, err)
		}
		return webinterror(w, errstr)
	}

	stats := getstorstats()
	atomic.AddInt64(&stats.bytesloaded, bytes)
	return nil
}
