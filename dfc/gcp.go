/*
 * Copyright (c) 2017, NVIDIA CORPORATION. All rights reserved.
 *
 */
package dfc

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
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
func (obj *gcpif) getobj(w http.ResponseWriter, mpath string, bucket string, objname string) error {
	fname := mpath + "/" + bucket + "/" + objname

	projid, errstr := getProjID()
	if projid == "" {
		return webinterror(w, errstr)
	}
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		glog.Fatal(err)
	}
	o := client.Bucket(bucket).Object(objname)
	attrs, err := o.Attrs(ctx)
	if err != nil {
		errstr := fmt.Sprintf("Failed to get attributes for object %s from bucket %s, err: %v", objname, bucket, err)
		return webinterror(w, errstr)
	}
	omd5 := hex.EncodeToString(attrs.MD5)
	rc, err := o.NewReader(ctx)
	if err != nil {
		errstr := fmt.Sprintf("Failed to create rc for object %s to file %q, err: %v", objname, fname, err)
		return webinterror(w, errstr)
	}
	defer rc.Close()
	file, err := createfile(mpath, bucket, objname)
	if err != nil {
		errstr := fmt.Sprintf("Failed to create file %q, err: %v", fname, err)
		return webinterror(w, errstr)
	} else {
		glog.Infof("Created file %q", fname)
	}
	defer file.Close()
	hash := md5.New()
	writer := io.MultiWriter(file, hash)
	bytes, err := io.Copy(writer, rc)
	if err != nil {
		errstr := fmt.Sprintf("Failed to download object %s to file %q, err: %v", objname, fname, err)
		return webinterror(w, errstr)
		// FIXME: checksetmounterror() - see aws.go
	}
	hashInBytes := hash.Sum(nil)[:16]
	fmd5 := hex.EncodeToString(hashInBytes)
	if omd5 != fmd5 {
		errstr := fmt.Sprintf("Object's %s MD5sum %v does not match with file(%s)'s MD5sum %v",
			objname, omd5, fname, fmd5)
		// Remove downloaded file.
		err := os.Remove(fname)
		if err != nil {
			glog.Errorf("Failed to delete file %s, err: %v", fname, err)
		}
		return webinterror(w, errstr)
	} else {
		glog.Infof("Object's %s MD5sum %v does MATCH with file(%s)'s MD5sum %v",
			objname, omd5, fname, fmd5)
	}

	stats := getstorstats()
	atomic.AddInt64(&stats.bytesloaded, bytes)
	return nil
}

func (obj *gcpif) putobj(r *http.Request, w http.ResponseWriter,
	bucket string, kname string) error {

	projid, errstr := getProjID()
	if projid == "" {
		return webinterror(w, errstr)
	}
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		errstr := fmt.Sprintf("Failed to create client for bucket %s object %s , err: %v",
			bucket, kname, err)
		return webinterror(w, errstr)
	}

	wc := client.Bucket(bucket).Object(kname).NewWriter(ctx)
	defer wc.Close()
	_, err = io.Copy(wc, r.Body)
	if err != nil {
		errstr := fmt.Sprintf("Failed to upload object %s into bucket %s , err: %v",
			kname, bucket, err)
		return webinterror(w, errstr)
	}
	return nil
}
