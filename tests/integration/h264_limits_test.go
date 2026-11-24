package integration

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Explicit isolated-backend acceptance, alongside TestLibVNCH264Backend.
func h264TestEndpoint(t *testing.T) (string, []byte) {
	t.Helper()
	address := os.Getenv("SVNC_RFB_H264_ADDRESS")
	if address == "" {
		t.Skip("set SVNC_RFB_H264_ADDRESS for isolated H.264 acceptance")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("acceptance requires loopback")
	}
	password, err := os.ReadFile(os.Getenv("SVNC_RFB_PASSWORD_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	return address, bytes.TrimRight(password, "\r\n")
}

func h264TestFrame(t *testing.T, p *rfbProbe, encodings []uint32, want uint32) []byte {
	t.Helper()
	p.SetDeadline(time.Now().Add(15 * time.Second))
	if encodings != nil {
		message := []byte{2, 0, 0, byte(len(encodings))}
		for _, encoding := range encodings {
			message = binary.BigEndian.AppendUint32(message, encoding)
		}
		if _, err := p.Write(message); err != nil {
			t.Fatal(err)
		}
	}
	request := []byte{3, 0, 0, 0, 0, 0}
	request = binary.BigEndian.AppendUint16(request, p.width)
	request = binary.BigEndian.AppendUint16(request, p.height)
	if _, err := p.Write(request); err != nil {
		t.Fatal(err)
	}
	read := func(size int) []byte {
		data := make([]byte, size)
		if _, err := io.ReadFull(p, data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	for {
		kind := read(1)[0]
		if kind == 3 {
			header := read(7)
			length := binary.BigEndian.Uint32(header[3:])
			if length > 1024*1024 {
				t.Fatal("invalid clipboard length")
			}
			read(int(length))
			continue
		}
		header := read(3)
		if kind != 0 || binary.BigEndian.Uint16(header[1:]) != 1 {
			t.Fatal("expected one rectangle")
		}
		rectangle := read(12)
		if binary.BigEndian.Uint32(rectangle[8:]) != want {
			t.Fatalf("expected encoding %d, got %d", want, binary.BigEndian.Uint32(rectangle[8:]))
		}
		if want == 0 {
			return read(int(p.width) * int(p.height) * 4)
		}
		video := read(8)
		length := binary.BigEndian.Uint32(video)
		flags := uint32(0)
		if encodings != nil {
			flags = 2
		}
		if length == 0 || length > 16*1024*1024 || binary.BigEndian.Uint32(video[4:]) != flags {
			t.Fatal("new encoder did not return a bounded reset access unit")
		}
		return read(int(length))
	}
}

func TestLibVNCH264ClientLimitAndReconnect(t *testing.T) {
	address, password := h264TestEndpoint(t)
	connect := func() *rfbProbe {
		t.Helper()
		p, err := connectRFB(address, password, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Close() })
		return p
	}
	first, second, third := connect(), connect(), connect()
	h264TestFrame(t, first, []uint32{50, 0}, 50)
	h264TestFrame(t, second, []uint32{50, 0}, 50)
	h264TestFrame(t, third, []uint32{50, 0}, 0)
	// Renegotiating to Raw must release the slot without disconnecting.
	h264TestFrame(t, first, []uint32{0}, 0)
	h264TestFrame(t, third, []uint32{50, 0}, 50)
	// EOFs are processed by the event loop before the subsequent handshake and
	// framebuffer round trip; no sleeps or leaked encoder state are needed.
	second.Close()
	third.Close()
	h264TestFrame(t, first, []uint32{0}, 0)
	fresh := connect()
	h264TestFrame(t, fresh, []uint32{50, 0}, 50)
}

func TestLibVNCH264RawPreference(t *testing.T) {
	address, password := h264TestEndpoint(t)
	p, err := connectRFB(address, password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// A supported earlier encoding must win over H.264, and ordinary clients
	// must still obtain a complete framebuffer.
	h264TestFrame(t, p, []uint32{0, 50}, 0)
	h264TestFrame(t, p, []uint32{50, 0}, 50)
	h264TestFrame(t, p, []uint32{0}, 0)
}

// Decode the actual SSH/RFB payload, including inter-frame references. FFmpeg is
// an explicit acceptance dependency; normal unit tests never invoke it.
func TestLibVNCH264PayloadDecodes(t *testing.T) {
	address, password := h264TestEndpoint(t)
	if os.Getenv("SVNC_TEST_H264_DECODE") != "1" {
		t.Skip("set SVNC_TEST_H264_DECODE=1 with FFmpeg installed")
	}
	p, err := connectRFB(address, password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var stream bytes.Buffer
	stream.Write(h264TestFrame(t, p, []uint32{50, 0}, 50))
	for i := 0; i < 3; i++ {
		stream.Write(h264TestFrame(t, p, nil, 50))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "h264", "-i", "pipe:0", "-f", "framemd5", "pipe:1")
	command.Stdin = &stream
	var errors bytes.Buffer
	command.Stderr = &errors
	output, err := command.Output()
	if err != nil || errors.Len() != 0 {
		t.Fatalf("H.264 decoder: %v %s", err, errors.String())
	}
	count := 0
	for _, line := range strings.Split(string(output), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			count++
		}
	}
	if count != 4 {
		t.Fatalf("decoded %d frames, want 4", count)
	}
}

func TestLibVNCH264UnsupportedGeometry(t *testing.T) {
	if os.Getenv("SVNC_TEST_H264_ODD_GEOMETRY") != "1" {
		t.Skip("requires isolated odd-sized Xvfb")
	}
	address, password := h264TestEndpoint(t)
	p, err := connectRFB(address, password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.width%2 == 0 && p.height%2 == 0 {
		t.Fatal("acceptance requires an odd framebuffer size")
	}
	h264TestFrame(t, p, []uint32{50, 0}, 0)
}
