package integration

import (
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

type rfbProbe struct {
	net.Conn
	width, height uint16
}

func connectRFB(address string, password []byte, forceNone bool) (*rfbProbe, error) {
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		return nil, err
	}
	p := &rfbProbe{Conn: conn}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	fail := func(err error) (*rfbProbe, error) { conn.Close(); return nil, err }
	banner := make([]byte, 12)
	if _, err = io.ReadFull(conn, banner); err != nil {
		return fail(err)
	}
	if string(banner) != "RFB 003.008\n" {
		return fail(fmt.Errorf("unexpected RFB banner"))
	}
	if _, err = conn.Write(banner); err != nil {
		return fail(err)
	}
	var count [1]byte
	if _, err = io.ReadFull(conn, count[:]); err != nil {
		return fail(err)
	}
	types := make([]byte, int(count[0]))
	if _, err = io.ReadFull(conn, types); err != nil {
		return fail(err)
	}
	if len(types) != 1 || types[0] != 2 {
		return fail(fmt.Errorf("expected only VNC authentication, got %v", types))
	}
	choice := byte(2)
	if forceNone {
		choice = 1
	}
	conn.Write([]byte{choice})
	if !forceNone {
		challenge := make([]byte, 16)
		if _, err = io.ReadFull(conn, challenge); err != nil {
			return fail(err)
		}
		key := make([]byte, 8)
		for i := 0; i < len(password) && i < 8; i++ {
			key[i] = bits.Reverse8(password[i])
		}
		cipher, err := des.NewCipher(key)
		if err != nil {
			return fail(err)
		}
		response := make([]byte, 16)
		cipher.Encrypt(response[:8], challenge[:8])
		cipher.Encrypt(response[8:], challenge[8:])
		conn.Write(response)
	}
	var status [4]byte
	if _, err = io.ReadFull(conn, status[:]); err != nil {
		return fail(err)
	}
	if binary.BigEndian.Uint32(status[:]) != 0 {
		return fail(fmt.Errorf("authentication rejected"))
	}
	conn.Write([]byte{1})
	init := make([]byte, 24)
	if _, err = io.ReadFull(conn, init); err != nil {
		return fail(err)
	}
	p.width = binary.BigEndian.Uint16(init)
	p.height = binary.BigEndian.Uint16(init[2:])
	n := binary.BigEndian.Uint32(init[20:])
	if n > 4096 {
		return fail(fmt.Errorf("invalid desktop name length"))
	}
	name := make([]byte, n)
	if _, err = io.ReadFull(conn, name); err != nil {
		return fail(err)
	}
	if !strings.HasPrefix(string(name), "SVNC / LibVNCServer") {
		return fail(fmt.Errorf("unexpected desktop backend"))
	}
	return p, nil
}

// Run explicitly against a staged native backend through a local SSH tunnel.
// No host discovery, credentials, or desktop actions are part of normal tests.
func TestLibVNCBackend(t *testing.T) {
	address := os.Getenv("SVNC_RFB_ADDRESS")
	if address == "" {
		t.Skip("set SVNC_RFB_ADDRESS and SVNC_RFB_PASSWORD_FILE for native backend acceptance")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("acceptance requires a loopback SSH tunnel")
	}
	password, err := os.ReadFile(os.Getenv("SVNC_RFB_PASSWORD_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	password = []byte(strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"))
	for _, tc := range []struct {
		name     string
		password []byte
		none     bool
	}{
		{"wrong_password", []byte("bad-test"), false}, {"noauth_not_offered", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := connectRFB(address, tc.password, tc.none)
			if err == nil {
				p.Close()
				t.Fatal("unauthorized client reached desktop initialization")
			}
		})
	}
	t.Run("rfb_file_transfer_disabled", func(t *testing.T) {
		p, err := connectRFB(address, password, false)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		// UltraVNC FileTransferAccess. The backend must close this path before
		// accepting any directory listing or file request.
		p.Write([]byte{7, 14, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
		for {
			var kind [1]byte
			_, err := io.ReadFull(p, kind[:])
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind[0] != 3 {
				t.Fatalf("unexpected response to disabled file transfer: %d", kind[0])
			}
			// A legitimately authenticated peer may receive pending clipboard text.
			header := make([]byte, 7)
			if _, err := io.ReadFull(p, header); err != nil {
				t.Fatal(err)
			}
			n := binary.BigEndian.Uint32(header[3:])
			if n > 1024*1024 {
				t.Fatal("invalid clipboard length")
			}
			if _, err := io.CopyN(io.Discard, p, int64(n)); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("incomplete_handshake_reclaimed", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(13 * time.Second))
		banner := make([]byte, 12)
		if _, err := io.ReadFull(conn, banner); err != nil {
			t.Fatal(err)
		}
		var data [1]byte
		if _, err := conn.Read(data[:]); !errors.Is(err, io.EOF) {
			t.Fatalf("unauthenticated connection was not reclaimed cleanly: %v", err)
		}
	})
	t.Run("fragmented_negotiation_raw_cursor_clipboard", func(t *testing.T) {
		p, err := connectRFB(address, password, false)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		// 32-bit little-endian BGRX.
		format := []byte{0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}
		p.Write(format)
		encodings := []int32{0, -239, -232, -223, -1063131698}
		msg := make([]byte, 4+4*len(encodings))
		msg[0] = 2
		binary.BigEndian.PutUint16(msg[2:], uint16(len(encodings)))
		for i, e := range encodings {
			binary.BigEndian.PutUint32(msg[4+i*4:], uint32(e))
		}
		p.Write(msg[:4])
		time.Sleep(30 * time.Millisecond)
		p.Write(msg[4:])
		p.Write([]byte{3, 0, 0, 0, 0, 0, 0, 64, 0, 64})
		raw, cursor, caps := false, false, false
		for !(raw && cursor && caps) {
			var kind [1]byte
			if _, err := io.ReadFull(p, kind[:]); err != nil {
				t.Fatalf("raw=%v cursor=%v clipboard_caps=%v: %v", raw, cursor, caps, err)
			}
			switch kind[0] {
			case 0:
				header := make([]byte, 3)
				if _, err := io.ReadFull(p, header); err != nil {
					t.Fatal(err)
				}
				count := binary.BigEndian.Uint16(header[1:])
				for i := 0; i < int(count); i++ {
					rect := make([]byte, 12)
					if _, err := io.ReadFull(p, rect); err != nil {
						t.Fatal(err)
					}
					w, h := int(binary.BigEndian.Uint16(rect[4:])), int(binary.BigEndian.Uint16(rect[6:]))
					e := int32(binary.BigEndian.Uint32(rect[8:]))
					n := 0
					switch e {
					case 0:
						raw = true
						n = w * h * 4
					case -239:
						cursor = true
						n = w*h*4 + ((w+7)/8)*h
					case -232, -223:
					default:
						t.Fatalf("unexpected encoding %d", e)
					}
					if n > 64*1024*1024 {
						t.Fatal("unbounded rectangle")
					}
					if _, err := io.CopyN(io.Discard, p, int64(n)); err != nil {
						t.Fatal(err)
					}
				}
			case 3:
				header := make([]byte, 7)
				if _, err := io.ReadFull(p, header); err != nil {
					t.Fatal(err)
				}
				length := int32(binary.BigEndian.Uint32(header[3:]))
				if length >= 0 {
					if _, err := io.CopyN(io.Discard, p, int64(length)); err != nil {
						t.Fatal(err)
					}
					continue
				}
				n := -int64(length)
				if n < 4 || n > 1024*1024 {
					t.Fatal("invalid clipboard envelope")
				}
				payload := make([]byte, n)
				if _, err := io.ReadFull(p, payload); err != nil {
					t.Fatal(err)
				}
				caps = binary.BigEndian.Uint32(payload)&(1<<24) != 0 || caps
			default:
				t.Fatalf("unexpected message %d", kind[0])
			}
		}
		t.Logf("authenticated %dx%d desktop; Raw, cursor and ExtendedClipboard negotiated", p.width, p.height)
	})
}
