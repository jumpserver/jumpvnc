package main

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestMeasuredConnCountsVNCBytes(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	var received, sent atomic.Int64
	conn := &measuredConn{Conn: local, received: &received, sent: &sent}
	done := make(chan error, 1)
	go func() {
		if _, err := remote.Write([]byte("frame")); err != nil {
			done <- err
			return
		}
		_, err := io.ReadFull(remote, make([]byte, 3))
		done <- err
	}()
	if _, err := io.ReadFull(conn, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("key")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if received.Load() != 5 || sent.Load() != 3 {
		t.Fatalf("received=%d sent=%d", received.Load(), sent.Load())
	}
}

func TestPerformanceRejectsOldConnectionAndExpiresRTT(t *testing.T) {
	now := time.Now()
	s := &session{id: "new", ctx: context.Background(), started: now.Add(-time.Second), rtt: 12 * time.Millisecond, rttAt: now}
	s.received.Store(123)
	d := &Desktop{current: s}
	if _, err := d.Performance("old"); err == nil {
		t.Fatal("returned metrics for a different connection")
	}
	p := s.performance(now)
	if p.ElapsedSeconds != 1 || p.ReceivedBytes != 123 || p.RTTMillis == nil || *p.RTTMillis != 12 {
		t.Fatalf("unexpected sample: %+v", p)
	}
	if p := s.performance(now.Add(16 * time.Second)); p.RTTMillis != nil {
		t.Fatal("reported a stale RTT")
	}
	s.rttAt = time.Time{}
	if p := s.performance(now); p.RTTMillis != nil {
		t.Fatal("reported an RTT before the first successful probe")
	}
}
