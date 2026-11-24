package integration

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// Explicit native-backend acceptance. Reads frames but never changes the desktop,
// injects input, or writes clipboard contents. Normal tests do not connect.
func TestLibVNCH264Backend(t *testing.T) {
	address := os.Getenv("SVNC_RFB_H264_ADDRESS")
	if address == "" {
		t.Skip("set SVNC_RFB_H264_ADDRESS and SVNC_RFB_PASSWORD_FILE for H.264 acceptance")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("acceptance requires a loopback SSH tunnel")
	}
	password, err := os.ReadFile(os.Getenv("SVNC_RFB_PASSWORD_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := connectRFB(address, bytes.TrimRight(password, "\r\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	setEncodings := func(encodings ...uint32) {
		message := []byte{2, 0, 0, byte(len(encodings))}
		for _, encoding := range encodings {
			message = binary.BigEndian.AppendUint32(message, encoding)
		}
		if _, err := p.Write(message); err != nil {
			t.Fatal(err)
		}
	}
	request := func() {
		p.SetDeadline(time.Now().Add(20 * time.Second))
		message := make([]byte, 10)
		message[0] = 3 // non-incremental full-screen refresh
		binary.BigEndian.PutUint16(message[6:], p.width)
		binary.BigEndian.PutUint16(message[8:], p.height)
		if _, err := p.Write(message); err != nil {
			t.Fatal(err)
		}
	}
	readRect := func(want uint32) ([]byte, uint32) {
		for {
			var kind [1]byte
			if _, err := io.ReadFull(p, kind[:]); err != nil {
				t.Fatal(err)
			}
			if kind[0] == 3 { // optional legacy ServerCutText; discard without logging
				var header [7]byte
				if _, err := io.ReadFull(p, header[:]); err != nil {
					t.Fatal(err)
				}
				n := binary.BigEndian.Uint32(header[3:])
				if n > 1024*1024 {
					t.Fatal("invalid clipboard length")
				}
				if _, err := io.CopyN(io.Discard, p, int64(n)); err != nil {
					t.Fatal(err)
				}
				continue
			}
			var update [3]byte
			if _, err := io.ReadFull(p, update[:]); err != nil {
				t.Fatal(err)
			}
			if kind[0] != 0 || binary.BigEndian.Uint16(update[1:]) != 1 {
				t.Fatalf("expected one rectangle, got message %d header %v", kind[0], update)
			}
			var rectangle [12]byte
			if _, err := io.ReadFull(p, rectangle[:]); err != nil {
				t.Fatal(err)
			}
			if binary.BigEndian.Uint32(rectangle[8:]) != want ||
				binary.BigEndian.Uint16(rectangle[4:]) != p.width ||
				binary.BigEndian.Uint16(rectangle[6:]) != p.height {
				t.Fatalf("unexpected rectangle %v", rectangle)
			}
			if want == 0 {
				if _, err := io.CopyN(io.Discard, p, int64(p.width)*int64(p.height)*4); err != nil {
					t.Fatal(err)
				}
				return nil, 0
			}
			var header [8]byte
			if _, err := io.ReadFull(p, header[:]); err != nil {
				t.Fatal(err)
			}
			n := binary.BigEndian.Uint32(header[:])
			if n > 16*1024*1024 {
				t.Fatal("invalid H.264 length")
			}
			data := make([]byte, n)
			if _, err := io.ReadFull(p, data); err != nil {
				t.Fatal(err)
			}
			return data, binary.BigEndian.Uint32(header[4:])
		}
	}
	for cycle := 0; cycle < 2; cycle++ {
		// Encoding 50 must win only when explicitly advertised before Tight/Raw.
		setEncodings(50, 7, 0)
		for frame := 0; frame < 2; frame++ {
			request()
			data, flags := readRect(50)
			if (flags&2 != 0) != (frame == 0) {
				t.Fatalf("unexpected context reset flags %d", flags)
			}
			if len(data) == 0 {
				t.Fatal("low-latency encoder returned no frame")
			}
			if frame == 0 {
				var sps, pps, idr bool
				for i := 0; i+4 < len(data); i++ {
					if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
						switch data[i+3] & 31 {
						case 7:
							sps = true
						case 8:
							pps = true
						case 5:
							idr = true
						}
					}
				}
				if !sps || !pps || !idr {
					t.Fatal("first video frame lacks SPS/PPS/IDR")
				}
			}
			t.Logf("cycle=%d frame=%d H.264 bytes=%d reset=%d", cycle, frame, len(data), flags)
		}
		setEncodings(0)
		request()
		readRect(0)
	}
}
