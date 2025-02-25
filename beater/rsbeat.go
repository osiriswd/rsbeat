package beater

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/elastic/beats/libbeat/beat"
	"github.com/elastic/beats/libbeat/common"
	"github.com/elastic/beats/libbeat/logp"
	"github.com/elastic/beats/libbeat/publisher"

	"github.com/garyburd/redigo/redis"
	"github.com/osiriswd/rsbeat/config"
)

type Rsbeat struct {
	done          chan struct{}
	config        config.Config
	client        publisher.Client
	lastIndexTime time.Time //test
	poolList      map[string]*redis.Pool
}

// Creates beater
func New(b *beat.Beat, cfg *common.Config) (beat.Beater, error) {
	config := config.DefaultConfig
	if err := cfg.Unpack(&config); err != nil {
		return nil, fmt.Errorf("Error reading config file: %v", err)
	}

	logp.Info("config.Redis: %v", config.Redis)
	logp.Info("config.slowerThan: %v", config.SlowerThan)
	logp.Info("config.password: %v", config.Password)
	var poolList = make(map[string]*redis.Pool)
	for _, ipPort := range config.Redis {
		poolList[ipPort] = poolInit(ipPort, config.SlowerThan, config.Password)
		logp.Info("redis: %s", ipPort)
	}

	bt := &Rsbeat{
		done:     make(chan struct{}),
		config:   config,
		poolList: poolList,
	}

	return bt, nil
}

func (bt *Rsbeat) Run(b *beat.Beat) error {
	logp.Info("rsbeat is running! Hit CTRL-C to stop it.")

	bt.client = b.Publisher.Connect()
	ticker := time.NewTicker(bt.config.Period)
	for {
		select {
		case <-bt.done:
			return nil
		case <-ticker.C:
		}

		for ipPort, pool := range bt.poolList {
			logp.Info("Event sent instance:%s", ipPort)
			go bt.redisc(b.Name, true, pool.Get(), ipPort)
		}
		bt.lastIndexTime = time.Now()
	}
}

func (bt *Rsbeat) Stop() {
	bt.client.Close()
	close(bt.done)
}

type itemLog struct {
	slowId     int
	timestamp  int64
	duration   int
	cmd        string
	key        string
	args       string
	clientinfo string
	clientname string
}

func (bt *Rsbeat) redisc(beatname string, init bool, c redis.Conn, ipPort string) {
	defer c.Close()
	logp.Info("conn:%v", c)

	c.Send("SLOWLOG", "GET")
	c.Send("SLOWLOG", "RESET")
	logp.Info("redis: slowlog get. slowlog reset")

	c.Flush()
	reply, err := redis.Values(c.Receive()) // reply from GET
	c.Receive()                             // reply from RESET

	logp.Info("reply len: %d", len(reply))

	for _, i := range reply {
		rp, _ := redis.Values(i, err)
		var itemLog itemLog
		var args []string
		redis.Scan(rp, &itemLog.slowId, &itemLog.timestamp, &itemLog.duration, &args, &itemLog.clientinfo, &itemLog.clientname)
		argsLen := len(args)
		if argsLen >= 1 {
			itemLog.cmd = args[0]
		}
		if argsLen >= 2 {
			itemLog.key = args[1]
		}
		if argsLen >= 3 {
			g, _ := json.Marshal(args[2:])
			itemLog.args = string(g)
		}
		logp.Info("timestamp is: %d", itemLog.timestamp)
		t := time.Unix(itemLog.timestamp, 0).UTC()

		s := strings.Split(itemLog.clientinfo, ":")
		event := common.MapStr{
			"type":           beatname,
			"@timestamp":     common.Time(time.Now()),
			"@log_timestamp": common.Time(t),
			"slow_id":        itemLog.slowId,
			"cmd":            itemLog.cmd,
			"key":            itemLog.key,
			"args":           itemLog.args,
			"duration":       itemLog.duration,
			"ip_port":        ipPort,
			"clientip":       s[0],
			"clientport":     s[1],
			"clientname":     itemLog.clientname,
		}

		bt.client.PublishEvent(event)
	}
}

func poolInit(server string, slowerThan int, password string) *redis.Pool {
	return &redis.Pool{
		MaxIdle:     3,
		MaxActive:   3,
		IdleTimeout: 240 * time.Second,
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("tcp", server, redis.DialConnectTimeout(3*time.Second), redis.DialReadTimeout(3*time.Second))
			if err != nil {
				logp.Err("redis: error occurs when connect %v", err.Error())
				return nil, err
			}
			if password != "" {
				if _, err := c.Do("AUTH", password); err != nil {
					c.Close()
					return nil, err
				}
			}
			c.Send("MULTI")
			if slowerThan > 0 {
				c.Send("CONFIG", "SET", "slowlog-log-slower-than", slowerThan)
				c.Send("CONFIG", "SET", "slowlog-max-len", 500)
			}
			c.Send("SLOWLOG", "RESET")
			r, err := c.Do("EXEC")

			if err != nil {
				logp.Err("redis: error occurs when send config set %v", err.Error())
				return nil, err
			}

			logp.Info("redis: config set %v", r)
			return c, err
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			logp.Info("redis: PING")
			return err
		},
	}
}
