package main

import (
	"errors"
	"net"
	"sync/atomic"
	"time"
)

type Performance struct {
	ID             string   `json:"id"`
	ElapsedSeconds float64  `json:"elapsedSeconds"`
	ReceivedBytes  int64    `json:"receivedBytes"`
	SentBytes      int64    `json:"sentBytes"`
	RTTMillis      *float64 `json:"rttMillis"`
}

// Performance only counts the VNC channel, excluding SFTP and SSH overhead.
func (d *Desktop) Performance(id string) (Performance, error) {
	s, err := d.active()
	if err != nil {
		return Performance{}, err
	}
	if s.id != id {
		return Performance{}, errors.New("连接已更换")
	}
	return s.performance(time.Now()), nil
}

func (s *session) performance(now time.Time) Performance {
	p := Performance{
		ID: s.id, ElapsedSeconds: now.Sub(s.started).Seconds(),
		ReceivedBytes: s.received.Load(), SentBytes: s.sent.Load(),
	}
	s.rttMu.RLock()
	defer s.rttMu.RUnlock()
	if !s.rttAt.IsZero() && now.Sub(s.rttAt) <= 15*time.Second {
		millis := float64(s.rtt) / float64(time.Millisecond)
		p.RTTMillis = &millis
	}
	return p
}

type measuredConn struct {
	net.Conn
	received *atomic.Int64
	sent     *atomic.Int64
}

func (c *measuredConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.received.Add(int64(n))
	return n, err
}

func (c *measuredConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.sent.Add(int64(n))
	return n, err
}
