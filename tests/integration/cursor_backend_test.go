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

// Explicit read-only acceptance against a staged backend, through SSH.
// This never changes the pointer position, keyboard, or clipboard.
func TestLibVNCAlphaCursor(t *testing.T) {
	address := os.Getenv("SVNC_RFB_ALPHA_ADDRESS")
	if address == "" {
		t.Skip("set SVNC_RFB_ALPHA_ADDRESS and SVNC_RFB_PASSWORD_FILE for alpha-cursor acceptance")
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
	const vmware uint32 = 0x574d5664
	const rich uint32 = 0xffffff11
	for _, alpha := range []bool{true, false, true} {
		encodings := []uint32{0, rich}
		want := rich
		if alpha {
			encodings = []uint32{0, vmware, rich}
			want = vmware
		}
		message := []byte{2, 0, 0, byte(len(encodings))}
		for _, encoding := range encodings {
			message = binary.BigEndian.AppendUint32(message, encoding)
		}
		p.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := p.Write(message); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Write([]byte{3, 0, 0, 0, 0, 0, 0, 64, 0, 64}); err != nil {
			t.Fatal(err)
		}
		found := false
		for !found {
			var kind [1]byte
			if _, err := io.ReadFull(p, kind[:]); err != nil {
				t.Fatal(err)
			}
			if kind[0] == 3 {
				var header [7]byte
				if _, err := io.ReadFull(p, header[:]); err != nil {
					t.Fatal(err)
				}
				n := int64(int32(binary.BigEndian.Uint32(header[3:])))
				if n < 0 {
					n = -n
				}
				if n > 1024*1024 {
					t.Fatal("invalid clipboard envelope")
				}
				if _, err := io.CopyN(io.Discard, p, n); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if kind[0] != 0 {
				t.Fatalf("unexpected message %d", kind[0])
			}
			var header [3]byte
			if _, err := io.ReadFull(p, header[:]); err != nil {
				t.Fatal(err)
			}
			for n := binary.BigEndian.Uint16(header[1:]); n > 0; n-- {
				var rect [12]byte
				if _, err := io.ReadFull(p, rect[:]); err != nil {
					t.Fatal(err)
				}
				w, h := int(binary.BigEndian.Uint16(rect[4:])), int(binary.BigEndian.Uint16(rect[6:]))
				encoding := binary.BigEndian.Uint32(rect[8:])
				size := w * h * 4
				switch encoding {
				case 0:
				case rich, vmware:
					if encoding != want {
						t.Fatalf("cursor encoding %#x, want %#x", encoding, want)
					}
					if w > 256 || h > 256 || (w > 0 && int(binary.BigEndian.Uint16(rect[:])) >= w) ||
						(h > 0 && int(binary.BigEndian.Uint16(rect[2:])) >= h) {
						t.Fatal("invalid cursor geometry")
					}
					found = true
					if encoding == rich {
						size += ((w + 7) / 8) * h
					} else {
						var subtype [2]byte
						if _, err := io.ReadFull(p, subtype[:]); err != nil {
							t.Fatal(err)
						}
						if subtype != [2]byte{1, 0} {
							t.Fatal("expected straight RGBA alpha cursor")
						}
						data := make([]byte, size)
						if _, err := io.ReadFull(p, data); err != nil {
							t.Fatal(err)
						}
						partial := 0
						for i := 3; i < len(data); i += 4 {
							if data[i] > 0 && data[i] < 255 {
								partial++
							}
						}
						t.Logf("alpha cursor %dx%d: %d partially transparent pixels", w, h, partial)
						size = 0
					}
				default:
					t.Fatalf("unexpected rectangle encoding %#x", encoding)
				}
				if size > 64*1024*1024 {
					t.Fatal("unbounded rectangle")
				}
				if _, err := io.CopyN(io.Discard, p, int64(size)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
