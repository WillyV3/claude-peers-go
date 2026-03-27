package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

// NATSMonitorData is the aggregated NATS monitoring snapshot.
type NATSMonitorData struct {
	Server      NATSServerInfo   `json:"server"`
	Connections []NATSConnInfo   `json:"connections"`
	Streams     []NATSStreamInfo `json:"streams"`
	Timestamp   string           `json:"timestamp"`
}

type NATSServerInfo struct {
	Version      string `json:"version"`
	Uptime       string `json:"uptime"`
	Connections  int    `json:"connections"`
	TotalConns   int    `json:"total_connections"`
	InMsgs       int64  `json:"in_msgs"`
	OutMsgs      int64  `json:"out_msgs"`
	InBytes      int64  `json:"in_bytes"`
	OutBytes     int64  `json:"out_bytes"`
	SlowConsumers int   `json:"slow_consumers"`
	MemUsed      int64  `json:"mem_used"`
	MemMax       int64  `json:"mem_max"`
	StoreUsed    int64  `json:"store_used"`
	StoreMax     int64  `json:"store_max"`
	NumStreams    int    `json:"num_streams"`
	NumConsumers int    `json:"num_consumers"`
	NumMessages  int64  `json:"num_messages"`
	APITotal     int64  `json:"api_total"`
	APIErrors    int64  `json:"api_errors"`
}

type NATSConnInfo struct {
	Name    string `json:"name"`
	IP      string `json:"ip"`
	Lang    string `json:"lang"`
	InMsgs  int64  `json:"in_msgs"`
	OutMsgs int64  `json:"out_msgs"`
	Subs    int    `json:"subscriptions"`
	Pending int    `json:"pending"`
}

type NATSStreamInfo struct {
	Name          string             `json:"name"`
	Messages      int64              `json:"messages"`
	Bytes         int64              `json:"bytes"`
	ConsumerCount int                `json:"consumer_count"`
	FirstSeq      int64              `json:"first_seq"`
	LastSeq       int64              `json:"last_seq"`
	Consumers     []NATSConsumerInfo `json:"consumers"`
}

type NATSConsumerInfo struct {
	Name       string `json:"name"`
	AckPending int    `json:"ack_pending"`
	Delivered  int64  `json:"delivered"`
	NumPending int64  `json:"num_pending"`
}

func (gw *Gridwatch) collectNATSMonitor(ctx context.Context) {
	base := gw.config.NATSMonitorURL
	client := &http.Client{Timeout: 5 * time.Second}

	for {
		data := gw.fetchNATSMonitor(client, base)
		raw, _ := json.Marshal(data)
		gw.natsMonMu.Lock()
		gw.natsMonCache = raw
		gw.natsMonMu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (gw *Gridwatch) fetchNATSMonitor(client *http.Client, base string) NATSMonitorData {
	d := NATSMonitorData{Timestamp: time.Now().UTC().Format(time.RFC3339)}

	// /varz
	var varz struct {
		Version       string `json:"version"`
		Uptime        string `json:"uptime"`
		Connections   int    `json:"connections"`
		TotalConns    int    `json:"total_connections"`
		InMsgs        int64  `json:"in_msgs"`
		OutMsgs       int64  `json:"out_msgs"`
		InBytes       int64  `json:"in_bytes"`
		OutBytes      int64  `json:"out_bytes"`
		SlowConsumers int    `json:"slow_consumers"`
		Mem           int64  `json:"mem"`
	}
	if err := natsMonitorGet(client, base+"/varz", &varz); err != nil {
		log.Printf("[nats-mon] varz: %v", err)
	} else {
		d.Server.Version = varz.Version
		d.Server.Uptime = varz.Uptime
		d.Server.Connections = varz.Connections
		d.Server.TotalConns = varz.TotalConns
		d.Server.InMsgs = varz.InMsgs
		d.Server.OutMsgs = varz.OutMsgs
		d.Server.InBytes = varz.InBytes
		d.Server.OutBytes = varz.OutBytes
		d.Server.SlowConsumers = varz.SlowConsumers
		d.Server.MemUsed = varz.Mem
	}

	// /connz
	var connz struct {
		Connections []struct {
			Name    string `json:"name"`
			IP      string `json:"ip"`
			Lang    string `json:"lang"`
			InMsgs  int64  `json:"in_msgs"`
			OutMsgs int64  `json:"out_msgs"`
			Subs    int    `json:"num_subscriptions"`
			Pending int    `json:"pending_bytes"`
		} `json:"connections"`
	}
	if err := natsMonitorGet(client, base+"/connz", &connz); err != nil {
		log.Printf("[nats-mon] connz: %v", err)
	} else {
		for _, c := range connz.Connections {
			d.Connections = append(d.Connections, NATSConnInfo{
				Name:    c.Name,
				IP:      c.IP,
				Lang:    c.Lang,
				InMsgs:  c.InMsgs,
				OutMsgs: c.OutMsgs,
				Subs:    c.Subs,
				Pending: c.Pending,
			})
		}
	}

	// /jsz?streams=true&consumers=true
	var jsz struct {
		Memory    int64 `json:"memory"`
		Store     int64 `json:"store"`
		MemLimit  int64 `json:"memory_max_bytes"`
		StoreLimit int64 `json:"store_max_bytes"`
		Streams   int   `json:"streams"`
		Consumers int   `json:"consumers"`
		Messages  int64 `json:"messages"`
		Bytes     int64 `json:"bytes"`
		API       struct {
			Total  int64 `json:"total"`
			Errors int64 `json:"errors"`
		} `json:"api"`
		AccountDetail []struct {
			Streams []struct {
				Name string `json:"name"`
				State struct {
					Messages  int64 `json:"messages"`
					Bytes     int64 `json:"bytes"`
					FirstSeq  int64 `json:"first_seq"`
					LastSeq   int64 `json:"last_seq"`
					Consumers int   `json:"consumer_count"`
				} `json:"state"`
				Consumer []struct {
					Name          string `json:"name"`
					NumAckPending int    `json:"num_ack_pending"`
					Delivered     struct {
						Consumer int64 `json:"consumer_seq"`
					} `json:"delivered"`
					NumPending int64 `json:"num_pending"`
				} `json:"consumer_detail"`
			} `json:"stream_detail"`
		} `json:"account_details"`
	}
	if err := natsMonitorGet(client, base+"/jsz?streams=true&consumers=true&acc-details=true", &jsz); err != nil {
		log.Printf("[nats-mon] jsz: %v", err)
	} else {
		d.Server.MemUsed = jsz.Memory
		d.Server.MemMax = jsz.MemLimit
		d.Server.StoreUsed = jsz.Store
		d.Server.StoreMax = jsz.StoreLimit
		d.Server.NumStreams = jsz.Streams
		d.Server.NumConsumers = jsz.Consumers
		d.Server.NumMessages = jsz.Messages
		d.Server.APITotal = jsz.API.Total
		d.Server.APIErrors = jsz.API.Errors

		for _, acc := range jsz.AccountDetail {
			for _, s := range acc.Streams {
				si := NATSStreamInfo{
					Name:          s.Name,
					Messages:      s.State.Messages,
					Bytes:         s.State.Bytes,
					ConsumerCount: s.State.Consumers,
					FirstSeq:      s.State.FirstSeq,
					LastSeq:       s.State.LastSeq,
				}
				for _, c := range s.Consumer {
					si.Consumers = append(si.Consumers, NATSConsumerInfo{
						Name:       c.Name,
						AckPending: c.NumAckPending,
						Delivered:  c.Delivered.Consumer,
						NumPending: c.NumPending,
					})
				}
				d.Streams = append(d.Streams, si)
			}
		}
	}

	return d
}

func natsMonitorGet(client *http.Client, url string, out any) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (gw *Gridwatch) handleNATSMonitor(w http.ResponseWriter, r *http.Request) {
	gw.natsMonMu.RLock()
	data := gw.natsMonCache
	gw.natsMonMu.RUnlock()
	if data == nil {
		data = []byte(`{"server":{},"connections":[],"streams":[],"timestamp":""}`)
	}
	jsonResponse(w, data)
}
