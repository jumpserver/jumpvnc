// SPDX-License-Identifier: GPL-2.0-or-later
// Windows interactive-desktop backend. File operations are served by the Go Agent over SFTP.
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <winsock2.h>
#include <windows.h>
#include <algorithm>
#include <atomic>
#include <cstdio>
#include <d3d11.h>
#include <dxgi1_2.h>
#include <map>
#include <rfb/keysym.h>
#include <rfb/rfb.h>
#include <set>
#include <string>
#include <vector>
#include <wrl/client.h>
using Microsoft::WRL::ComPtr;
#include "runtime.h"
struct ClientInput {
    std::set<WORD> keys;
    int buttons = 0;
    ULONGLONG created = GetTickCount64();
};
static std::map<WORD, unsigned> keyOwners;
static unsigned buttonOwners[3]{};
static unsigned clientCount = 0;
static int originX, originY;
static void keyInput(WORD key, bool down, bool unicode = false) {
    INPUT i{};
    i.type = INPUT_KEYBOARD;
    i.ki.wVk = unicode ? 0 : key;
    i.ki.wScan = unicode ? key : 0;
    i.ki.dwFlags = (down ? 0 : KEYEVENTF_KEYUP) | (unicode ? KEYEVENTF_UNICODE : 0);
    if (!unicode && (key == VK_RCONTROL || key == VK_RMENU || (key >= VK_PRIOR && key <= VK_DOWN) ||
                     key == VK_INSERT || key == VK_DELETE || key == VK_LWIN || key == VK_RWIN))
        i.ki.dwFlags |= KEYEVENTF_EXTENDEDKEY;
    SendInput(1, &i, sizeof(i));
}
static void keyboard(rfbBool down, rfbKeySym sym, rfbClientPtr cl) {
    if (!desktopAvailable || viewOnly || cl->viewOnly || !cl->clientData)
        return;
    auto &state = *static_cast<ClientInput *>(cl->clientData);
    WORD vk = 0;
    if (sym >= XK_F1 && sym <= XK_F24)
        vk = VK_F1 + (sym - XK_F1);
    else
        switch (sym) {
        case XK_BackSpace:
            vk = VK_BACK;
            break;
        case XK_Tab:
        case XK_ISO_Left_Tab:
            vk = VK_TAB;
            break;
        case XK_Return:
        case XK_KP_Enter:
            vk = VK_RETURN;
            break;
        case XK_Escape:
            vk = VK_ESCAPE;
            break;
        case XK_Insert:
            vk = VK_INSERT;
            break;
        case XK_Delete:
            vk = VK_DELETE;
            break;
        case XK_Home:
            vk = VK_HOME;
            break;
        case XK_End:
            vk = VK_END;
            break;
        case XK_Page_Up:
            vk = VK_PRIOR;
            break;
        case XK_Page_Down:
            vk = VK_NEXT;
            break;
        case XK_Left:
            vk = VK_LEFT;
            break;
        case XK_Up:
            vk = VK_UP;
            break;
        case XK_Right:
            vk = VK_RIGHT;
            break;
        case XK_Down:
            vk = VK_DOWN;
            break;
        case XK_Shift_L:
            vk = VK_LSHIFT;
            break;
        case XK_Shift_R:
            vk = VK_RSHIFT;
            break;
        case XK_Control_L:
            vk = VK_LCONTROL;
            break;
        case XK_Control_R:
            vk = VK_RCONTROL;
            break;
        case XK_Alt_L:
            vk = VK_LMENU;
            break;
        case XK_Alt_R:
            vk = VK_RMENU;
            break;
        case XK_Super_L:
        case XK_Meta_L:
            vk = VK_LWIN;
            break;
        case XK_Super_R:
        case XK_Meta_R:
            vk = VK_RWIN;
            break;
        case XK_Caps_Lock:
            vk = VK_CAPITAL;
            break;
        case XK_Num_Lock:
            vk = VK_NUMLOCK;
            break;
        case XK_Print:
            vk = VK_SNAPSHOT;
            break;
        case XK_Pause:
            vk = VK_PAUSE;
            break;
        default:
            if (sym >= 32 && sym < 127) {
                SHORT k = VkKeyScanW((WCHAR)sym);
                if (k != -1)
                    vk = LOBYTE(k);
            }
        }
    if (vk) {
        if (down) {
            if (state.keys.insert(vk).second)
                ++keyOwners[vk];
            keyInput(vk, true);
        } else if (state.keys.erase(vk) && --keyOwners[vk] == 0) {
            keyInput(vk, false);
            keyOwners.erase(vk);
        }
    } else if (down) {
        unsigned cp = (sym & 0xff000000) == 0x01000000 ? sym & 0xffffff : sym;
        if (cp >= 32 && cp <= 0x10ffff && !(cp >= 0xd800 && cp <= 0xdfff)) {
            if (cp <= 0xffff) {
                keyInput((WORD)cp, true, true);
                keyInput((WORD)cp, false, true);
            } else {
                cp -= 0x10000;
                for (WORD c : {(WORD)(0xd800 + (cp >> 10)), (WORD)(0xdc00 + (cp & 1023))}) {
                    keyInput(c, true, true);
                    keyInput(c, false, true);
                }
            }
        }
    }
}
static void pointer(int mask, int x, int y, rfbClientPtr cl) {
    if (!desktopAvailable || viewOnly || cl->viewOnly || !cl->clientData || x < 0 || y < 0 ||
        x >= cl->screen->width || y >= cl->screen->height)
        return;
    auto &state = *static_cast<ClientInput *>(cl->clientData);
    int buttons = state.buttons;
    INPUT i{};
    i.type = INPUT_MOUSE;
    int vx = GetSystemMetrics(SM_XVIRTUALSCREEN), vy = GetSystemMetrics(SM_YVIRTUALSCREEN);
    int vw = GetSystemMetrics(SM_CXVIRTUALSCREEN), vh = GetSystemMetrics(SM_CYVIRTUALSCREEN);
    i.mi.dx = (LONG)((long long)(x + originX - vx) * 65535 / std::max(1, vw - 1));
    i.mi.dy = (LONG)((long long)(y + originY - vy) * 65535 / std::max(1, vh - 1));
    i.mi.dwFlags = MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK;
    SendInput(1, &i, sizeof(i));
    const DWORD downs[] = {MOUSEEVENTF_LEFTDOWN, MOUSEEVENTF_MIDDLEDOWN, MOUSEEVENTF_RIGHTDOWN};
    const DWORD ups[] = {MOUSEEVENTF_LEFTUP, MOUSEEVENTF_MIDDLEUP, MOUSEEVENTF_RIGHTUP};
    for (int b = 0; b < 3; b++)
        if ((mask ^ buttons) & (1 << b)) {
            bool down = (mask & (1 << b)) != 0;
            bool emit = down ? buttonOwners[b]++ == 0 : buttonOwners[b] && --buttonOwners[b] == 0;
            if (emit) {
                i = {};
                i.type = INPUT_MOUSE;
                i.mi.dwFlags = down ? downs[b] : ups[b];
                SendInput(1, &i, sizeof(i));
            }
        }
    if ((mask & 24) && !(buttons & 24)) {
        i = {};
        i.type = INPUT_MOUSE;
        i.mi.dwFlags = MOUSEEVENTF_WHEEL;
        i.mi.mouseData = (mask & 8) ? WHEEL_DELTA : (DWORD)-WHEEL_DELTA;
        SendInput(1, &i, sizeof(i));
    }
    state.buttons = mask;
    rfbDefaultPtrAddEvent(mask, x, y, cl);
}
static void releaseKeys(rfbClientPtr cl) {
    if (!cl || !cl->clientData)
        return;
    auto &state = *static_cast<ClientInput *>(cl->clientData);
    for (WORD key : state.keys) {
        if (--keyOwners[key] == 0) {
            keyInput(key, false);
            keyOwners.erase(key);
        }
    }
    state.keys.clear();
    const DWORD ups[] = {MOUSEEVENTF_LEFTUP, MOUSEEVENTF_MIDDLEUP, MOUSEEVENTF_RIGHTUP};
    for (int b = 0; b < 3; ++b)
        if ((state.buttons & (1 << b)) && buttonOwners[b] && --buttonOwners[b] == 0) {
            INPUT i{};
            i.type = INPUT_MOUSE;
            i.mi.dwFlags = ups[b];
            SendInput(1, &i, sizeof(i));
        }
    state.buttons = 0;
}
static void disconnected(rfbClientPtr cl) {
    releaseKeys(cl);
    delete static_cast<ClientInput *>(cl->clientData);
    cl->clientData = nullptr;
    if (clientCount)
        --clientCount;
}
static enum rfbNewClientAction connected(rfbClientPtr cl) {
    if (clientCount >= 16)
        return RFB_CLIENT_REFUSE;
    cl->clientData = new ClientInput();
    cl->clientGoneHook = disconnected;
    cl->viewOnly = viewOnly ? TRUE : FALSE;
    ++clientCount;
    return RFB_CLIENT_ACCEPT;
}
struct Capture {
    ComPtr<ID3D11Device> device;
    ComPtr<ID3D11DeviceContext> context;
    ComPtr<IDXGIOutputDuplication> duplication;
    ComPtr<ID3D11Texture2D> staging;
    int width = 0, height = 0;
    bool init() {
        duplication.Reset();
        staging.Reset();
        context.Reset();
        device.Reset();
        ComPtr<IDXGIFactory1> factory;
        if (FAILED(CreateDXGIFactory1(IID_PPV_ARGS(&factory))))
            return false;
        HMONITOR primary = MonitorFromPoint(POINT{0, 0}, MONITOR_DEFAULTTOPRIMARY);
        for (UINT a = 0;; a++) {
            ComPtr<IDXGIAdapter1> adapter;
            if (factory->EnumAdapters1(a, &adapter) == DXGI_ERROR_NOT_FOUND)
                break;
            for (UINT o = 0;; o++) {
                ComPtr<IDXGIOutput> output;
                if (adapter->EnumOutputs(o, &output) == DXGI_ERROR_NOT_FOUND)
                    break;
                DXGI_OUTPUT_DESC desc{};
                output->GetDesc(&desc);
                if (desc.Monitor != primary || !desc.AttachedToDesktop)
                    continue;
                if (desc.Rotation != DXGI_MODE_ROTATION_IDENTITY) {
                    fprintf(stderr, "Rotated displays are not supported by this trial.\n");
                    return false;
                }
                D3D_FEATURE_LEVEL level;
                HRESULT hr = D3D11CreateDevice(adapter.Get(), D3D_DRIVER_TYPE_UNKNOWN, nullptr, 0, nullptr, 0,
                                               D3D11_SDK_VERSION, &device, &level, &context);
                if (FAILED(hr))
                    return false;
                ComPtr<IDXGIOutput1> output1;
                if (FAILED(output.As(&output1)))
                    return false;
                hr = output1->DuplicateOutput(device.Get(), &duplication);
                if (FAILED(hr)) {
                    fprintf(stderr, "DuplicateOutput failed: 0x%08lx\n", hr);
                    return false;
                }
                DXGI_OUTDUPL_DESC dd{};
                duplication->GetDesc(&dd);
                width = (int)dd.ModeDesc.Width;
                height = (int)dd.ModeDesc.Height;
                if (width < 1 || height < 1 || width > 16384 || height > 16384 ||
                    (size_t)width * height > 64000000)
                    return false;
                originX = captureOriginX = desc.DesktopCoordinates.left;
                originY = captureOriginY = desc.DesktopCoordinates.top;
                D3D11_TEXTURE2D_DESC td{};
                td.Width = width;
                td.Height = height;
                td.MipLevels = 1;
                td.ArraySize = 1;
                td.Format = DXGI_FORMAT_B8G8R8A8_UNORM;
                td.SampleDesc.Count = 1;
                td.Usage = D3D11_USAGE_STAGING;
                td.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
                return SUCCEEDED(device->CreateTexture2D(&td, nullptr, &staging));
            }
        }
        return false;
    }
    int update(rfbScreenInfoPtr screen) {
        DXGI_OUTDUPL_FRAME_INFO info{};
        ComPtr<IDXGIResource> resource;
        HRESULT hr = duplication->AcquireNextFrame(0, &info, &resource);
        if (hr == DXGI_ERROR_WAIT_TIMEOUT)
            return 0;
        if (FAILED(hr))
            return -1;
        if (info.LastPresentTime.QuadPart == 0) {
            duplication->ReleaseFrame();
            return 0;
        }
        ComPtr<ID3D11Texture2D> texture;
        hr = resource.As(&texture);
        if (FAILED(hr)) {
            duplication->ReleaseFrame();
            return -1;
        }
        context->CopyResource(staging.Get(), texture.Get());
        D3D11_MAPPED_SUBRESOURCE map{};
        hr = context->Map(staging.Get(), 0, D3D11_MAP_READ, 0, &map);
        if (FAILED(hr)) {
            duplication->ReleaseFrame();
            return -1;
        }
        // Coalesce changed 64x64 tiles into horizontal runs. Never send unchanged pixels.
        for (int y = 0; y < height; y += 64) {
            int y2 = std::min(y + 64, height), run = -1;
            for (int x = 0; x < width; x += 64) {
                int x2 = std::min(x + 64, width);
                bool changed = false;
                for (int row = y; row < y2 && !changed; row++)
                    changed =
                        memcmp(screen->frameBuffer + ((size_t)row * width + x) * 4,
                               (char *)map.pData + (size_t)row * map.RowPitch + x * 4, (x2 - x) * 4) != 0;
                if (changed) {
                    for (int row = y; row < y2; row++)
                        memcpy(screen->frameBuffer + ((size_t)row * width + x) * 4,
                               (char *)map.pData + (size_t)row * map.RowPitch + x * 4, (x2 - x) * 4);
                    if (run < 0)
                        run = x;
                } else if (run >= 0) {
                    rfbMarkRectAsModified(screen, run, y, x, y2);
                    run = -1;
                }
            }
            if (run >= 0)
                rfbMarkRectAsModified(screen, run, y, width, y2);
        }
        context->Unmap(staging.Get(), 0);
        duplication->ReleaseFrame();
        return 1;
    }
};
static void serverFormat(rfbScreenInfoPtr screen) {
    screen->serverFormat.redShift = 16;
    screen->serverFormat.greenShift = 8;
    screen->serverFormat.blueShift = 0;
    screen->serverFormat.redMax = screen->serverFormat.greenMax = screen->serverFormat.blueMax = 255;
    screen->serverFormat.depth = 24;
    auto it = rfbGetClientIterator(screen);
    while (auto cl = rfbClientIteratorNext(it))
        screen->setTranslateFunction(cl);
    rfbReleaseClientIterator(it);
}
static std::atomic<bool> running{true};
static BOOL WINAPI control(DWORD) {
    running = false;
    return TRUE;
}
int wmain(int argc, wchar_t **argv) {
    SetProcessDPIAware();
    setvbuf(stdout, nullptr, _IONBF, 0);
    setvbuf(stderr, nullptr, _IONBF, 0);
    Options options;
    if (!parseOptions(argc, argv, options)) {
        fprintf(stderr, "Usage: svnc-desktop --listen 127.0.0.1:PORT --password-file PATH [--view-only] "
                        "[--parent-pid PID]\n");
        return 1;
    }
    viewOnly = options.viewOnly;
    std::string password;
    if (!readPassword(options.passwordFile, password)) {
        fprintf(stderr, "Password file must contain 1-8 printable ASCII bytes and an optional newline.\n");
        return 1;
    }
    HANDLE parent = nullptr;
    if (options.parentPid) {
        parent = OpenProcess(SYNCHRONIZE, FALSE, options.parentPid);
        if (!parent) {
            fprintf(stderr, "Cannot monitor Agent parent process.\n");
            return 1;
        }
    }
    Capture capture;
    if (!capture.init()) {
        fprintf(stderr, "Cannot capture current interactive user desktop.\n");
        return 2;
    }
    // Do not pass application arguments to LibVNCServer's permissive argument parser.
    int rfbArgc = 1;
    char name[] = "svnc-desktop";
    char *rfbArgv[] = {name, nullptr};
    rfbScreenInfoPtr s = rfbGetScreen(&rfbArgc, rfbArgv, capture.width, capture.height, 8, 3, 4);
    if (!s)
        return 3;
    std::vector<char> pixels((size_t)capture.width * capture.height * 4);
    s->frameBuffer = pixels.data();
    serverFormat(s);
    s->desktopName = "SuperVNC Server / LibVNCServer / Windows DXGI";
    s->listenInterface = htonl(INADDR_LOOPBACK);
    s->port = options.port;
    s->autoPort = FALSE;
#ifdef LIBVNCSERVER_IPv6
    s->ipv6port = -1;
#endif
    s->httpPort = -1;
    s->httpDir = nullptr;
    s->alwaysShared = TRUE;
    // Both legacy UltraVNC file transfer and optional TightVNC extensions stay disabled.
    s->permitFileTransfer = FALSE;
    char *passwords[] = {password.data(), nullptr};
    s->authPasswdData = passwords;
    s->passwordCheck = rfbCheckPasswordByList;
    s->kbdAddEvent = keyboard;
    s->ptrAddEvent = pointer;
    s->kbdReleaseAllKeys = releaseKeys;
    s->newClientHook = connected;
    s->setXCutText = cutText;
    s->setXCutTextUTF8 = cutTextUTF8;
    s->deferUpdateTime = 5;
    s->deferPtrUpdateTime = 0;
    s->handleEventsEagerly = TRUE;
    s->maxClientWait = 2000;
    rfbInitServer(s);
    if (s->listenSock == RFB_INVALID_SOCKET) {
        rfbScreenCleanup(s);
        return 4;
    }
    clipboardWindow = CreateWindowExW(0, L"STATIC", L"SuperVNC Clipboard", 0, 0, 0, 0, 0, HWND_MESSAGE, nullptr,
                                      GetModuleHandle(nullptr), nullptr);
    if (!clipboardWindow) {
        fprintf(stderr, "Cannot initialize clipboard owner.\n");
        rfbShutdownServer(s, TRUE);
        rfbScreenCleanup(s);
        return 6;
    }
    clipboardSequence = GetClipboardSequenceNumber();
    SetConsoleCtrlHandler(control, TRUE);
    printf("READY backend=libvncserver version=%s primary=%dx%d listen=127.0.0.1:%d view_only=%d "
           "file_transfer=sftp\n",
           LIBVNCSERVER_PACKAGE_VERSION, capture.width, capture.height, options.port, (int)viewOnly);
    ULONGLONG nextCapture = 0, nextClipboard = 0, nextRecovery = 0;
    bool captureReady = true;
    while (running && rfbIsActive(s)) {
        if (parent && WaitForSingleObject(parent, 0) != WAIT_TIMEOUT)
            break;
        MSG message;
        while (PeekMessageW(&message, nullptr, 0, 0, PM_REMOVE)) {
            TranslateMessage(&message);
            DispatchMessageW(&message);
        }
        ULONGLONG now = GetTickCount64();
        auto handshakes = rfbGetClientIterator(s);
        while (auto cl = rfbClientIteratorNext(handshakes)) {
            auto state = static_cast<ClientInput *>(cl->clientData);
            if (state && cl->state != rfbClientRec::RFB_NORMAL && now - state->created > 10000)
                rfbCloseClient(cl);
        }
        rfbReleaseClientIterator(handshakes);
        if (!captureReady && now >= nextRecovery) {
            nextRecovery = now + 1000;
            if (capture.init()) {
                if (capture.width != s->width || capture.height != s->height) {
                    std::vector<char> resized((size_t)capture.width * capture.height * 4);
                    // Clients unable to negotiate resize must reconnect instead of interpreting stale
                    // dimensions.
                    auto it = rfbGetClientIterator(s);
                    while (auto cl = rfbClientIteratorNext(it))
                        if (!cl->useNewFBSize)
                            rfbCloseClient(cl);
                    rfbReleaseClientIterator(it);
                    rfbNewFramebuffer(s, resized.data(), capture.width, capture.height, 8, 3, 4);
                    pixels.swap(resized);
                    serverFormat(s);
                }
                captureReady = desktopAvailable = true;
                printf("capture.ready primary=%dx%d\n", capture.width, capture.height);
            }
        }
        if (captureReady && s->clientHead && now >= nextCapture) {
            nextCapture = now + 16;
            if (capture.update(s) < 0) {
                captureReady = desktopAvailable = false;
                nextRecovery = now + 250;
                auto it = rfbGetClientIterator(s);
                while (auto cl = rfbClientIteratorNext(it))
                    releaseKeys(cl);
                rfbReleaseClientIterator(it);
                std::fill(pixels.begin(), pixels.end(), 0);
                rfbMarkRectAsModified(s, 0, 0, s->width, s->height);
                fprintf(stderr, "capture.unavailable: waiting for ordinary desktop/display recovery\n");
            }
        }
        if (s->clientHead)
            pollCursor(s);
        if (now >= nextClipboard) {
            nextClipboard = now + 100;
            pollClipboard(s);
        }
        // One owner for LibVNCServer and framebuffer memory; no concurrent writes during encoding.
        rfbProcessEvents(s, 1000);
    }
    rfbShutdownServer(s, TRUE);
    rfbScreenCleanup(s);
    DestroyWindow(clipboardWindow);
    if (parent)
        CloseHandle(parent);
    return 0;
}
