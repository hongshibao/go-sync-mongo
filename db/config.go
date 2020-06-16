package db

import (
	"time"

	mgo "gopkg.in/mgo.v2"
)

type Config struct {
	URI              string
	SSL              bool
	IgnoreApplyError bool
	Creds            mgo.Credential
	Timeout          time.Duration
}

func (p *Config) Load() error {
	return nil
}

func (p *Config) validate() error {
	return nil
}
