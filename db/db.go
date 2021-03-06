package db

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/spf13/viper"

	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type Connection struct {
	config    Config
	dialInfo  *mgo.DialInfo
	Session   *mgo.Session
	OplogChan chan bson.M
	Mutex     sync.Mutex
	Optime    bson.MongoTimestamp
	NOplog    uint64
	NDone     uint64
}

type ApplyOpsResponse struct {
	Ok     bool   `bson:"ok"`
	ErrMsg string `bson:"errmsg"`
}

type Oplog struct {
	Timestamp bson.MongoTimestamp `bson:"ts"`
	HistoryID int64               `bson:"h"`
	Version   int                 `bson:"v"`
	Operation string              `bson:"op"`
	Namespace string              `bson:"ns"`
	Object    bson.D              `bson:"o"`
	Query     bson.D              `bson:"o2"`
}

func NewConnection(config Config) (*Connection, error) {
	c := new(Connection)
	c.config = config
	c.OplogChan = make(chan bson.M, 1000)
	var err error

	if c.dialInfo, err = mgo.ParseURL(c.config.URI); err != nil {
		return nil, fmt.Errorf("cannot parse given URI %s due to error: %s",
			c.config.URI, err.Error())
	}

	if c.config.SSL {
		tlsConfig := &tls.Config{}
		tlsConfig.InsecureSkipVerify = true
		c.dialInfo.DialServer = func(addr *mgo.ServerAddr) (net.Conn, error) {
			conn, err := tls.Dial("tcp", addr.String(), tlsConfig)
			return conn, err
		}
	}

	timeout := c.config.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	c.dialInfo.Timeout = timeout
	c.dialInfo.Username = c.config.Creds.Username
	c.dialInfo.Password = c.config.Creds.Password
	c.dialInfo.Mechanism = c.config.Creds.Mechanism

	//session, err := mgo.DialWithTimeout(c.config.URI, time.Second*3)
	session, err := mgo.DialWithInfo(c.dialInfo)
	if err != nil {
		return nil, err
	}
	session.SetSocketTimeout(timeout)
	session.SetPrefetch(1.0)

	c.Session = session
	//c.Session.SetMode(mgo.SecondaryPreferred, true)
	c.Session.Login(&c.config.Creds)

	return c, nil
}

func (c *Connection) Databases() ([]string, error) {
	dbnames, err := c.Session.DatabaseNames()
	if err != nil {
		return nil, err
	}

	var slice []string

	for _, dbname := range dbnames {
		if dbname != "local" && dbname != "admin" {
			slice = append(slice, dbname)
		}
	}
	return slice, nil
}

func (c *Connection) databaseRegExs() ([]bson.RegEx, error) {
	dbnames, err := c.Session.DatabaseNames()
	if err != nil {
		return nil, err
	}

	var slice []bson.RegEx

	for _, dbname := range dbnames {
		if dbname != "local" && dbname != "admin" {
			slice = append(slice, bson.RegEx{Pattern: dbname + ".*"})
		}
	}
	return slice, nil
}

func (c *Connection) Push(oplog bson.M) {
	c.OplogChan <- oplog
	c.Mutex.Lock()
	defer c.Mutex.Unlock()
	c.NOplog++
}

func (c *Connection) SyncOplog(dst *Connection, tsRecorder TimestampRecorder) error {
	var (
		restoreQuery bson.M
		tailQuery    bson.M
		oplogEntry   Oplog
		iter         *mgo.Iter
		sec          bson.MongoTimestamp
		ord          bson.MongoTimestamp
		err          error
	)

	oplog := c.Session.DB("local").C("oplog.rs")

	var headResult struct {
		Timestamp bson.MongoTimestamp `bson:"ts"`
	}
	err = oplog.Find(nil).Sort("-$natural").Limit(1).One(&headResult)
	if err != nil {
		return fmt.Errorf("oplog find one failed: %v", err)
	}

	restoreQuery = bson.M{
		"ts": bson.M{"$gt": bson.MongoTimestamp(time.Now().Unix()<<32 + time.Now().Unix())},
	}

	tailQuery = bson.M{
		"ts": bson.M{"$gt": headResult.Timestamp},
	}

	var sinceTimestamp bson.MongoTimestamp
	if viper.GetInt("since") > 0 {
		sec = bson.MongoTimestamp(viper.GetInt("since"))
		ord = bson.MongoTimestamp(viper.GetInt("ordinal"))
		sinceTimestamp = (sec<<32 + ord)
		restoreQuery["ts"] = bson.M{"$gt": sinceTimestamp}
	}
	if tsRecorder != nil {
		recordTimestamp, err := tsRecorder.Read()
		if err != nil {
			log.Printf("read timestamp info from recorder failed: %v", err)
		} else if recordTimestamp > sinceTimestamp {
			log.Printf("Recorded timestamp (%v) is larger than since timestamp (%v), so recorded timestamp will be used",
				recordTimestamp, sinceTimestamp)
			restoreQuery["ts"] = bson.M{"$gt": recordTimestamp}
			sinceTimestamp = recordTimestamp
		} else {
			log.Printf("Recorded timestamp (%v) is not larger than since timestamp (%v), so since timestamp will be used",
				recordTimestamp, sinceTimestamp)
		}
	}

	dbnames, _ := c.databaseRegExs()
	if len(dbnames) > 0 {
		restoreQuery["ns"] = bson.M{"$in": dbnames}
		tailQuery["ns"] = bson.M{"$in": dbnames}
	} else {
		return fmt.Errorf("No databases found")
	}

	applyOpsResponse := ApplyOpsResponse{}
	opCount := 0

	if sinceTimestamp > 0 {
		log.Println("Restoring oplog...")
		iter = oplog.Find(restoreQuery).Iter()
		for iter.Next(&oplogEntry) {
			tailQuery = bson.M{
				"ts": bson.M{"$gte": oplogEntry.Timestamp},
			}

			// skip noops
			if oplogEntry.Operation == "n" {
				log.Printf("skipping no-op for namespace `%v`", oplogEntry.Namespace)
				continue
			}
			opCount++

			// apply the operation
			opsToApply := []Oplog{oplogEntry}
			err := dst.Session.Run(bson.M{"applyOps": opsToApply}, &applyOpsResponse)
			if err != nil {
				return fmt.Errorf("error applying ops: %v", err)
			}

			// check the server's response for an issue
			if !applyOpsResponse.Ok {
				if c.config.IgnoreApplyError {
					log.Println("ignore server error response of applying ops: %v",
						applyOpsResponse.ErrMsg)
				} else {
					return fmt.Errorf("server gave error applying ops: %v", applyOpsResponse.ErrMsg)
				}
			}

			log.Println(opCount)

			// update to timestamp recorder
			if tsRecorder != nil {
				err := tsRecorder.Write(oplogEntry.Timestamp)
				if err != nil {
					log.Printf("Timestamp recorder write failed: %v", err)
				}
			}
		}

		err = iter.Err()
		if err != nil {
			iter.Close()
			return err
		}
	}

	log.Println("Tailing...")
	iter = oplog.Find(tailQuery).Tail(1 * time.Second)
	for {
		for iter.Next(&oplogEntry) {
			// skip noops
			if oplogEntry.Operation == "n" {
				log.Printf("skipping no-op for namespace `%v`", oplogEntry.Namespace)
				continue
			}
			opCount++

			// apply the operation
			opsToApply := []Oplog{oplogEntry}
			err := dst.Session.Run(bson.M{"applyOps": opsToApply}, &applyOpsResponse)
			if err != nil {
				return fmt.Errorf("error applying ops: %v", err)
			}

			// check the server's response for an issue
			if !applyOpsResponse.Ok {
				if c.config.IgnoreApplyError {
					log.Println("ignore server error response of applying ops: %v",
						applyOpsResponse.ErrMsg)
				} else {
					return fmt.Errorf("server gave error applying ops: %v", applyOpsResponse.ErrMsg)
				}
			}

			log.Println(opCount)

			// update to timestamp recorder
			if tsRecorder != nil {
				err := tsRecorder.Write(oplogEntry.Timestamp)
				if err != nil {
					log.Printf("Timestamp recorder write failed: %v", err)
				}
			}
		}

		err = iter.Err()
		if err != nil {
			iter.Close()
			return err
		}

		if iter.Timeout() {
			if viper.GetBool("fast_stop") {
				iter.Close()
				return nil
			}
			continue
		}

		iter = oplog.Find(tailQuery).Tail(1 * time.Second)
	}
}
