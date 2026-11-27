package sshgateway

import (
	"context"
	"crypto/rand"
	"sort"
	"sync"
	"time"
)

type peerKey struct{}
type desktopPeer struct{ User, Address, Key string }

// RemoteConnection identifies a desktop channel, independently of SFTP channels.
type RemoteConnection struct {
	ID, User, Address       string
	Started                 time.Time
	ViewOnly, Authenticated bool
}

type trackedDesktop struct {
	RemoteConnection
	cancel context.CancelFunc
	proof  *rfbAuthProof
}

type Connections struct {
	mu     sync.Mutex
	active map[string]trackedDesktop
}

func (c *Connections) track(peer desktopPeer, viewOnly bool, proof *rfbAuthProof, cancel context.CancelFunc) func() {
	entry := trackedDesktop{RemoteConnection{rand.Text(), peer.User, peer.Address, time.Now(), viewOnly, false}, cancel, proof}
	c.mu.Lock()
	if c.active == nil {
		c.active = make(map[string]trackedDesktop)
	}
	c.active[entry.ID] = entry
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.active, entry.ID)
		c.mu.Unlock()
	}
}

func (c *Connections) List() []RemoteConnection {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]RemoteConnection, 0, len(c.active))
	for _, entry := range c.active {
		item := entry.RemoteConnection
		item.Authenticated = entry.proof.ok.Load()
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// The caller supplies the exact displayed IDs, including for "disconnect all".
func (c *Connections) Disconnect(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		if entry, ok := c.active[id]; ok {
			entry.cancel()
		}
	}
}
