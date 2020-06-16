package db

import (
	"fmt"
	"io/ioutil"
	"os"
	"strconv"

	"gopkg.in/mgo.v2/bson"
)

type TimestampRecorder interface {
	Write(ts bson.MongoTimestamp) error
	Read() (bson.MongoTimestamp, error)
}

type DiskTimestampRecorder struct {
	filepath string
}

func NewDiskTimestampRecorder(filepath string) (*DiskTimestampRecorder, error) {
	return &DiskTimestampRecorder{
		filepath: filepath,
	}, nil
}

func (r *DiskTimestampRecorder) Write(ts bson.MongoTimestamp) error {
	str := fmt.Sprintf("%v", ts)
	return ioutil.WriteFile(r.filepath, []byte(str), os.ModePerm)
}

func (r *DiskTimestampRecorder) Read() (bson.MongoTimestamp, error) {
	data, err := ioutil.ReadFile(r.filepath)
	if err != nil {
		return 0, err
	}
	ts, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return 0, err
	}
	return bson.MongoTimestamp(ts), nil
}
