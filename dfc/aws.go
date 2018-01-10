/*
 * Copyright (c) 2017, NVIDIA CORPORATION. All rights reserved.
 *
 */
package dfc

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/golang/glog"
)

func createsession() *session.Session {
	// TODO: avoid creating sessions for each request
	return session.Must(session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable}))

}
func (obj *awsif) listbucket(w http.ResponseWriter, bucket string) error {
	glog.Infof(" listbucket : bucket = %s ", bucket)
	sess := createsession()
	svc := s3.New(sess)
	params := &s3.ListObjectsInput{Bucket: aws.String(bucket)}
	resp, err := svc.ListObjects(params)
	if err != nil {
		return webinterror(w, err.Error())
	}
	// TODO: reimplement in JSON
	for _, key := range resp.Contents {
		glog.Infof("bucket = %s key = %s", bucket, *key.Key)
		keystr := fmt.Sprintf("%s", *key.Key)
		fmt.Fprintln(w, keystr)
	}
	return nil
}

func (obj *awsif) getobj(w http.ResponseWriter, mpath string, bktname string, keyname string) error {
	fname := mpath + "/" + bktname + "/" + keyname
	err := downloadobject(w, mpath, bktname, keyname)
	if err != nil {
		return webinterror(w, err.Error())
	}
	glog.Infof("Downloaded bucket %s key %s fqn %q", bktname, keyname, fname)
	return nil
}

// This function download S3 object into local file.
func downloadobject(w http.ResponseWriter,
	mpath string, bucket string, kname string) error {

	var file *os.File
	var err error
	var bytes int64

	fname := mpath + "/" + bucket + "/" + kname
	// strips the last part from filepath
	dirname := filepath.Dir(fname)
	if err = CreateDir(dirname); err != nil {
		glog.Errorf("Failed to create local dir %q, err: %s", dirname, err)
		return err
	}
	file, err = os.Create(fname)
	if err != nil {
		glog.Errorf("Unable to create file %q, err: %v", fname, err)
		checksetmounterror(fname)
		return err
	}
	sess := createsession()
	s3Svc := s3.New(sess)

	obj, err := s3Svc.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(kname),
	})
	defer obj.Body.Close()
	defer file.Close()
	if err != nil {
		glog.Errorf("Failed to download key %s from bucket %s, err: %v", kname, bucket, err)
		checksetmounterror(fname)
		return err
	}
	// Get ETag from object header
	omd5, _ := strconv.Unquote(*obj.ETag)

	hash := md5.New()
	writer := io.MultiWriter(file, hash)
	_, err = io.Copy(writer, obj.Body)

	if err != nil {
		glog.Errorf("Failed to copy obj key %s from bucket %s, err: %v", kname, bucket, err)
		checksetmounterror(fname)
		return err
	}
	hashInBytes := hash.Sum(nil)[:16]
	fmd5 := hex.EncodeToString(hashInBytes)
	if omd5 != fmd5 {
		errstr := fmt.Sprintf("Object's %s MD5sum %v does not match with file(%s)'s MD5sum %v",
			kname, omd5, fname, fmd5)
		glog.Error(errstr)
		err := os.Remove(fname)
		if err != nil {
			glog.Errorf("Failed to delete file %s, err: %v", fname, err)
		}
		checksetmounterror(fname)
		return errors.New(errstr)
	} else {
		glog.Infof("Object's %s MD5sum %v does MATCH with file(%s)'s MD5sum %v",
			kname, omd5, fname, fmd5)
	}

	stats := getstorstats()
	atomic.AddInt64(&stats.bytesloaded, bytes)
	return nil
}
