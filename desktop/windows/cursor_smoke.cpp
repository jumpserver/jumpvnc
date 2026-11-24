// SPDX-License-Identifier: GPL-2.0-or-later
// Tests cursor capture math and wire bytes without capturing the user's screen.
#define WIN32_LEAN_AND_MEAN
#include <winsock2.h>
#include "cursor_bridge.h"
#include "cursor_pixels.h"
#include <cstdio>
#include <cstdlib>
#include <vector>
static std::vector<unsigned char> wire;
static unsigned legacy = 0;
static bool flushed = false;
static void check(bool value, const char* message) {
    if (!value) { fprintf(stderr, "%s\n", message); exit(1); }
}
extern "C" rfbBool rfbSendCursorShape(rfbClientPtr) { ++legacy; return TRUE; }
extern "C" rfbBool rfbSendUpdateBuf(rfbClientPtr) { flushed = true; return TRUE; }
extern "C" int rfbWriteExact(rfbClientPtr, const char* data, int size) {
    check(flushed, "cursor metadata must follow the update header");
    wire.insert(wire.end(), data, data + size);
    return 1;
}
extern "C" void rfbCloseClient(rfbClientPtr) { check(false, "unexpected client close"); }
extern "C" void rfbStatRecordEncodingSent(rfbClientPtr, uint32_t, int, int) {}
extern "C" void rfbMakeRichCursorFromXCursor(rfbScreenInfoPtr, rfbCursorPtr) { check(false, "unexpected conversion"); }
static rfbCursorPtr cursorFor(rfbClientPtr cl) { return cl->screen->cursor; }
int main() {
    unsigned char black[] = {64, 32, 16, 0, 0, 0, 0, 0, 12, 34, 56, 0};
    const unsigned char white[] = {191, 159, 143, 0, 255, 255, 255, 0, 12, 34, 56, 0};
    unsigned char alpha[3]{};
    char mask[3]{};
    recoverCursorPixels(black, white, alpha, mask, 3);
    check(alpha[0] == 128 && alpha[1] == 0 && alpha[2] == 255, "lost partial alpha");
    check(black[0] == 128 && black[1] == 64 && black[2] == 32, "wrong unpremultiplication");
    check(mask[0] == 'x' && mask[1] == ' ' && mask[2] == 'x', "legacy mask");
    rfbCursor cursor{};
    cursor.width = 3; cursor.height = 1; cursor.xhot = 1;
    cursor.richSource = black; cursor.alphaSource = alpha;
    unsigned char packedMask[] = {0xa0}; cursor.mask = packedMask;
    rfbScreenInfo screen{};
    screen.cursor = &cursor; screen.getCursorPtr = cursorFor;
    screen.serverFormat.bitsPerPixel = 32;
    screen.serverFormat.redShift = 16; screen.serverFormat.greenShift = 8;
    screen.serverFormat.redMax = screen.serverFormat.greenMax = screen.serverFormat.blueMax = 255;
    rfbClientRec client{}, oldClient{};
    client.screen = oldClient.screen = &screen;
    svncCursorEnable(&client);
    check(client.enableCursorShapeUpdates && client.cursorWasChanged, "negotiation did not request shape");
    check(svncSendCursorShape(&client), "alpha send failed");
    const std::vector<unsigned char> expected = {
        0, 1, 0, 0, 0, 3, 0, 1, 0x57, 0x4d, 0x56, 0x64, 1, 0,
        32, 64, 128, 128, 0, 0, 0, 0, 56, 34, 12, 255};
    check(wire == expected, "incorrect VMware RGBA, hotspot or envelope");
    check(svncSendCursorShape(&oldClient) && legacy == 1, "legacy client was changed");
    svncCursorReset(&client);
    check(svncSendCursorShape(&client) && legacy == 2, "renegotiation kept stale alpha capability");
    svncCursorEnable(&client);
    screen.cursor = nullptr; wire.clear();
    check(svncSendCursorShape(&client) && wire.size() == 14 && wire[4] == 0 && wire[5] == 0, "hidden cursor");
    svncCursorReset(&client);
    puts("PASS: alpha recovery, straight RGBA wire bytes, hotspot, legacy isolation, reset and hidden cursor");
}
