#!/usr/bin/env python3
"""Exercise only our own loopback synthetic server; never probe g15 or another host."""
import argparse
import ctypes
import json
import socket
import struct
import subprocess
import tempfile
import time
from pathlib import Path

BASE = Path(__file__).resolve().parent
PASSWORD = b'evalonly'  # Public, disposable synthetic fixture credential.


def response(challenge):
    cc = ctypes.CDLL('/usr/lib/system/libcommonCrypto.dylib')
    cc.CCCrypt.argtypes = [ctypes.c_uint, ctypes.c_uint, ctypes.c_uint, ctypes.c_void_p,
                          ctypes.c_size_t, ctypes.c_void_p, ctypes.c_void_p, ctypes.c_size_t,
                          ctypes.c_void_p, ctypes.c_size_t, ctypes.POINTER(ctypes.c_size_t)]
    key = bytes(int(f'{v:08b}'[::-1], 2) for v in PASSWORD)
    output, size = ctypes.create_string_buffer(16), ctypes.c_size_t()
    assert cc.CCCrypt(0, 1, 2, key, 8, None, challenge, 16, output, 16, ctypes.byref(size)) == 0
    return output.raw[:size.value]


class Client:
    def __init__(self, port):
        self.socket = socket.create_connection(('127.0.0.1', port), timeout=2)
        self.socket.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        assert self.read(12) == b'RFB 003.008\n'
        self.socket.sendall(b'RFB 003.008\n')
        self.offered = self.read(self.read(1)[0])
        assert self.offered == b'\x02', self.offered

    def read(self, n):
        out = bytearray()
        while len(out) < n:
            data = self.socket.recv(n - len(out))
            if not data:
                raise EOFError('server closed connection')
            out.extend(data)
        return bytes(out)

    def authenticate(self, correct=True):
        self.socket.sendall(b'\x02')
        challenge = self.read(16)
        self.socket.sendall(response(challenge) if correct else bytes(16))
        return struct.unpack('!I', self.read(4))[0]

    def initialize(self):
        self.socket.sendall(b'\x01')
        width, height = struct.unpack('!HH', self.read(4))
        self.read(16)
        name = self.read(struct.unpack('!I', self.read(4))[0]).decode()
        assert (width, height) == (640, 360)
        assert name == 'svnc rustvncserver 2.2.1 synthetic evaluation'

    def request(self):
        self.socket.sendall(struct.pack('!BBHHHH', 3, 0, 0, 0, 640, 360))

    def raw_update(self):
        assert self.read(1) == b'\x00'
        self.read(1)
        count = struct.unpack('!H', self.read(2))[0]
        total = 0
        for _ in range(count):
            x, y, w, h, encoding = struct.unpack('!HHHHi', self.read(12))
            assert encoding == 0, encoding
            self.read(w * h * 4)
            total += w * h
        return total

    def close(self):
        self.socket.close()


def start_client(port):
    client = Client(port)
    assert client.authenticate() == 0
    client.initialize()
    return client


def fragmented(port, message, split_at):
    c = start_client(port)
    raw_encodings = struct.pack('!BBHi', 2, 0, 1, 0)
    if message != raw_encodings:
        c.socket.sendall(raw_encodings)
    c.socket.sendall(message[:split_at])
    time.sleep(0.12)  # Force a distinct read_buf() before the remainder arrives.
    c.socket.sendall(message[split_at:])
    c.request()
    c.socket.settimeout(0.8)
    try:
        return {'update_received': c.raw_update() > 0, 'error': None}
    except (TimeoutError, EOFError, ConnectionError, AssertionError) as error:
        return {'update_received': False, 'error': type(error).__name__}
    finally:
        c.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--server', type=Path, default=BASE / 'target/release/svnc-rustvncserver-evaluation')
    args = parser.parse_args()
    with socket.socket() as reservation:
        reservation.bind(('127.0.0.1', 0))
        port = reservation.getsockname()[1]
    with tempfile.TemporaryFile() as log:
        process = subprocess.Popen([str(args.server), str(port)], stdout=log, stderr=log)
        try:
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError('synthetic server exited')
                try:
                    with socket.create_connection(('127.0.0.1', port), timeout=.1):
                        break
                except OSError:
                    time.sleep(.02)
            else:
                raise RuntimeError('synthetic server did not become ready')
            results = {'crate': 'rustvncserver 2.2.1', 'scope': 'synthetic localhost only',
                       'patch': 'bind 127.0.0.1 instead of 0.0.0.0; no auth/parser changes'}
            c = start_client(port)
            c.socket.sendall(struct.pack('!BBHi', 2, 0, 1, 0))
            before = time.monotonic()
            c.request()
            results['normal_auth_and_raw_update'] = {'pixels': c.raw_update(),
                                                     'milliseconds': round((time.monotonic()-before)*1000, 1)}
            c.close()
            c = Client(port)
            results['wrong_password_rejected'] = c.authenticate(False) != 0
            c.close()
            c = Client(port)
            c.socket.sendall(b'\x01')  # Not in the server's advertised security types.
            accepted = struct.unpack('!I', c.read(4))[0] == 0
            if accepted:
                c.initialize()
                c.socket.sendall(struct.pack('!BBHi', 2, 0, 1, 0))
                c.request()
                results['unadvertised_noauth_reached_framebuffer'] = c.raw_update() > 0
            else:
                results['unadvertised_noauth_reached_framebuffer'] = False
            c.close()
            encodings = struct.pack('!BBHi', 2, 0, 1, 0)
            results['fragmented_set_encodings'] = fragmented(port, encodings, 4)
            text = b'fixture-text'
            cut_text = struct.pack('!BxxxI', 6, len(text)) + text
            c = start_client(port)
            c.socket.sendall(encodings + cut_text)
            c.request()
            results['unfragmented_cut_text_update_received'] = c.raw_update() > 0
            c.close()
            results['fragmented_cut_text'] = fragmented(port, cut_text, 8)
            print(json.dumps(results, indent=2))
        finally:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


if __name__ == '__main__':
    main()
