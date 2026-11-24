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
#include <iphlpapi.h>
#include <map>
#include <rfb/keysym.h>
#include <rfb/rfb.h>
#include <set>
#include <string>
#include <vector>
#include <wrl/client.h>
using Microsoft::WRL::ComPtr;
#include "runtime.h"
#include "h264_bridge.h"
#include "cursor_bridge.h"
#include "keyboard_input.h"
struct ClientInput {
    std::set<WORD> keys;
    int buttons = 0;
    ULONGLONG created = GetTickCount64();
};
static std::map<WORD, unsigned> keyOwners;
static unsigned buttonOwners[3]{};
static unsigned clientCount = 0;
static DWORD consoleAgentPid = 0;
static HANDLE sasRequest = nullptr;
static int originX, originY;
static bool inputAllowed(rfbClientPtr cl, const char *kind) {
    const bool active = onActiveDesktop();
    if (desktopAvailable && !viewOnly && !cl->viewOnly && cl->clientData && active)
        return true;
    static ULONGLONG nextLog = 0;
    if (GetTickCount64() >= nextLog) {
        nextLog = GetTickCount64() + 2000;
        wchar_t desktop[256]{};
        getDesktopName(GetThreadDesktop(GetCurrentThreadId()), desktop);
        fprintf(stderr, "input.blocked kind=%s available=%d server_view_only=%d client_view_only=%d active=%d desktop=%ls\n",
                kind, (int)desktopAvailable, (int)viewOnly, (int)cl->viewOnly, (int)active, desktop);
    }
    return false;
}
static bool injectInput(INPUT &input, const char *kind) {
    SetLastError(ERROR_SUCCESS);
    if (SendInput(1, &input, sizeof(input)) == 1) {
        static unsigned reported = 0;
        const unsigned bit = input.type == INPUT_MOUSE ? 1 : 2;
        if (!(reported & bit)) {
            reported |= bit;
            fprintf(stderr, "input.inject_ready kind=%s\n", kind);
        }
        return true;
    }
    const DWORD error = GetLastError();
    static ULONGLONG nextLog = 0;
    if (GetTickCount64() >= nextLog) {
        nextLog = GetTickCount64() + 2000;
        fprintf(stderr, "input.inject_failed kind=%s win32=%lu\n", kind, error);
    }
    return false;
}
static void keyInput(WORD key, bool down, bool unicode = false) {
    INPUT i = keyboardInput(key, down, unicode);
    injectInput(i, "keyboard");
}
static void keyboard(rfbBool down, rfbKeySym sym, rfbClientPtr cl) {
    if (!inputAllowed(cl, "keyboard"))
        return;
    auto &state = *static_cast<ClientInput *>(cl->clientData);
    WORD vk = virtualKeyForKeysym(sym);
    if (sasRequest && vk == VK_DELETE && (state.keys.count(VK_LCONTROL) || state.keys.count(VK_RCONTROL)) &&
        (state.keys.count(VK_LMENU) || state.keys.count(VK_RMENU))) {
        if (down)
            SetEvent(sasRequest);
        return;
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
        unsigned cp = textForKeysym(sym);
        if (cp) {
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
    if (!inputAllowed(cl, "pointer") || x < 0 || y < 0 ||
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
    injectInput(i, "pointer");
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
                injectInput(i, "button");
            }
        }
    if ((mask & 24) && !(buttons & 24)) {
        i = {};
        i.type = INPUT_MOUSE;
        i.mi.dwFlags = MOUSEEVENTF_WHEEL;
        i.mi.mouseData = (mask & 8) ? WHEEL_DELTA : (DWORD)-WHEEL_DELTA;
        injectInput(i, "wheel");
    }
    state.buttons = mask;
    rfbDefaultPtrAddEvent(mask, x, y, cl);
}
static void releaseKeys(rfbClientPtr cl) {
    if (!cl || !cl->clientData)
        return;
    auto &state = *static_cast<ClientInput *>(cl->clientData);
    const bool active = onActiveDesktop();
    for (WORD key : state.keys) {
        if (--keyOwners[key] == 0) {
            if (active)
                keyInput(key, false);
            keyOwners.erase(key);
        }
    }
    state.keys.clear();
    const DWORD ups[] = {MOUSEEVENTF_LEFTUP, MOUSEEVENTF_MIDDLEUP, MOUSEEVENTF_RIGHTUP};
    for (int b = 0; b < 3; ++b)
        if ((state.buttons & (1 << b)) && buttonOwners[b] && --buttonOwners[b] == 0 && active) {
            INPUT i{};
            i.type = INPUT_MOUSE;
            i.mi.dwFlags = ups[b];
            injectInput(i, "button_release");
        }
    state.buttons = 0;
}
static void disconnected(rfbClientPtr cl) {
    svncH264Reset(cl);
    svncCursorReset(cl);
    releaseKeys(cl);
    delete static_cast<ClientInput *>(cl->clientData);
    cl->clientData = nullptr;
    if (clientCount)
        --clientCount;
}
// A service backend can interact with Winlogon. Accept loopback connections
// only from the Agent so local processes also go through SSH authentication.
static bool fromConsoleAgent(rfbClientPtr cl) {
    sockaddr_in peer{}, local{};
    int peerSize = sizeof(peer), localSize = sizeof(local);
    if (getpeername(cl->sock, reinterpret_cast<sockaddr *>(&peer), &peerSize) ||
        getsockname(cl->sock, reinterpret_cast<sockaddr *>(&local), &localSize))
        return false;
    DWORD bytes = 0;
    if (GetExtendedTcpTable(nullptr, &bytes, FALSE, AF_INET, TCP_TABLE_OWNER_PID_CONNECTIONS, 0) !=
        ERROR_INSUFFICIENT_BUFFER)
        return false;
    std::vector<unsigned char> buffer;
    for (int attempt = 0; attempt < 3 && bytes <= 16 * 1024 * 1024; ++attempt) {
        buffer.resize(bytes);
        DWORD result = GetExtendedTcpTable(buffer.data(), &bytes, FALSE, AF_INET, TCP_TABLE_OWNER_PID_CONNECTIONS, 0);
        if (result == ERROR_INSUFFICIENT_BUFFER)
            continue;
        if (result != NO_ERROR)
            return false;
        const auto *table = reinterpret_cast<const MIB_TCPTABLE_OWNER_PID *>(buffer.data());
        for (DWORD i = 0; i < table->dwNumEntries; ++i) {
            const auto &row = table->table[i];
            if (row.dwOwningPid == consoleAgentPid && row.dwLocalAddr == peer.sin_addr.s_addr &&
                (USHORT)row.dwLocalPort == peer.sin_port && row.dwRemoteAddr == local.sin_addr.s_addr &&
                (USHORT)row.dwRemotePort == local.sin_port)
                return true;
        }
        return false;
    }
    return false;
}
static enum rfbNewClientAction connected(rfbClientPtr cl) {
    if (clientCount >= 16 || (consoleCapture && !fromConsoleAgent(cl)))
        return RFB_CLIENT_REFUSE;
    cl->clientData = new ClientInput();
    cl->clientGoneHook = disconnected;
    cl->viewOnly = viewOnly ? TRUE : FALSE;
    ++clientCount;
    return RFB_CLIENT_ACCEPT;
}
static bool validSize(int width, int height) {
    return width > 0 && height > 0 && width <= 16384 && height <= 16384 && (size_t)width * height <= 64000000;
}
struct GdiCapture {
    HDC screen = nullptr, memory = nullptr;
    HBITMAP bitmap = nullptr;
    HGDIOBJ previous = nullptr;
    void *pixels = nullptr;
    int width = 0, height = 0;

    ~GdiCapture() { reset(); }

    void reset() {
        if (previous && previous != HGDI_ERROR)
            SelectObject(memory, previous);
        if (bitmap)
            DeleteObject(bitmap);
        if (memory)
            DeleteDC(memory);
        if (screen)
            ReleaseDC(nullptr, screen);
        previous = nullptr;
        bitmap = nullptr;
        memory = screen = nullptr;
        pixels = nullptr;
    }

    bool init() {
        width = GetSystemMetrics(SM_CXSCREEN);
        height = GetSystemMetrics(SM_CYSCREEN);
        if (!validSize(width, height))
            return false;
        screen = GetDC(nullptr);
        if (!screen)
            return false;
        memory = CreateCompatibleDC(screen);
        if (!memory)
            return false;
        BITMAPINFO info{};
        info.bmiHeader.biSize = sizeof(info.bmiHeader);
        info.bmiHeader.biWidth = width;
        info.bmiHeader.biHeight = -height;
        info.bmiHeader.biPlanes = 1;
        info.bmiHeader.biBitCount = 32;
        info.bmiHeader.biCompression = BI_RGB;
        bitmap = CreateDIBSection(memory, &info, DIB_RGB_COLORS, &pixels, nullptr, 0);
        if (!bitmap || !pixels)
            return false;
        previous = SelectObject(memory, bitmap);
        return previous && previous != HGDI_ERROR && update();
    }

    bool update() const {
        return width == GetSystemMetrics(SM_CXSCREEN) && height == GetSystemMetrics(SM_CYSCREEN) &&
               BitBlt(memory, 0, 0, width, height, screen, 0, 0, SRCCOPY | CAPTUREBLT) && GdiFlush();
    }
};
struct Capture {
    ComPtr<ID3D11Device> device;
    ComPtr<ID3D11DeviceContext> context;
    ComPtr<IDXGIOutputDuplication> duplication;
    ComPtr<ID3D11Texture2D> staging;
    GdiCapture gdi;
    int width = 0, height = 0;
    void reset() {
        duplication.Reset();
        staging.Reset();
        context.Reset();
        device.Reset();
        gdi.reset();
    }
    bool init(bool allowGdi, bool gdiOnly = false) {
        reset();
        if (!gdiOnly && initDxgi())
            return true;
        reset();
        if (!allowGdi || !gdi.init()) {
            gdi.reset();
            return false;
        }
        width = gdi.width;
        height = gdi.height;
        originX = captureOriginX = originY = captureOriginY = 0;
        return true;
    }
    bool initDxgi() {
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
                if (!validSize(width, height))
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
        if (gdi.pixels) {
            if (!gdi.update())
                return -1;
            updatePixels(screen, static_cast<const char *>(gdi.pixels), (size_t)width * 4);
            return 1;
        }
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
        updatePixels(screen, static_cast<const char *>(map.pData), map.RowPitch);
        context->Unmap(staging.Get(), 0);
        duplication->ReleaseFrame();
        return 1;
    }
    void updatePixels(rfbScreenInfoPtr screen, const char *pixels, size_t pitch) const {
        // Coalesce changed 64x64 tiles into horizontal runs. Never send unchanged pixels.
        for (int y = 0; y < height; y += 64) {
            int y2 = std::min(y + 64, height), run = -1;
            for (int x = 0; x < width; x += 64) {
                int x2 = std::min(x + 64, width);
                bool changed = false;
                for (int row = y; row < y2 && !changed; row++)
                    changed =
                        memcmp(screen->frameBuffer + ((size_t)row * width + x) * 4,
                               pixels + (size_t)row * pitch + x * 4, (x2 - x) * 4) != 0;
                if (changed) {
                    for (int row = y; row < y2; row++)
                        memcpy(screen->frameBuffer + ((size_t)row * width + x) * 4,
                               pixels + (size_t)row * pitch + x * 4, (x2 - x) * 4);
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
static bool openClipboardOwner() {
    clipboardWindow = CreateWindowExW(0, L"STATIC", L"JumpVNC Clipboard", 0, 0, 0, 0, 0, HWND_MESSAGE, nullptr,
                                      GetModuleHandle(nullptr), nullptr);
    clipboardSequence = GetClipboardSequenceNumber();
    return clipboardWindow != nullptr;
}
static void closeClipboardOwner() {
    if (clipboardWindow)
        DestroyWindow(clipboardWindow);
    clipboardWindow = nullptr;
    pendingClipboard.clear();
    clipboardWriteDeadline = 0;
}
struct ConsoleDesktop {
    HDESK desktop = nullptr;

    bool follow(FILE *log) {
        if (desktop && onActiveDesktop())
            return true;
        closeClipboardOwner();
        // SendInput also needs playback access on the thread's desktop. A
        // read/write-only handle can capture frames but input fails with ERROR_ACCESS_DENIED.
        HDESK input = OpenInputDesktop(0, FALSE, DESKTOP_READOBJECTS | DESKTOP_WRITEOBJECTS |
                                                   DESKTOP_CREATEWINDOW | DESKTOP_JOURNALPLAYBACK);
        wchar_t name[256]{};
        if (!getDesktopName(input, name) || !SetThreadDesktop(input)) {
            if (input)
                CloseDesktop(input);
            return false;
        }
        if (desktop)
            CloseDesktop(desktop);
        desktop = input;
        if (!_wcsicmp(name, L"Default") && !openClipboardOwner())
            fprintf(stderr, "clipboard.unavailable win32=%lu\n", GetLastError());
        fprintf(log, "desktop.active name=%ls\n", name);
        return true;
    }
};
int wmain(int argc, wchar_t **argv) {
    SetProcessDPIAware();
    setvbuf(stdout, nullptr, _IONBF, 0);
    setvbuf(stderr, nullptr, _IONBF, 0);
    Options options;
    if (!parseOptions(argc, argv, options)) {
        fprintf(stderr, "Usage: svnc-desktop --listen 127.0.0.1:PORT --password-file PATH [--view-only] "
                        "[--parent-pid PID] [--console-session ID --log-file PATH --sas-event NAME]\n");
        return 1;
    }
    consoleCapture = options.consoleSession != 0;
    if (consoleCapture) {
        consoleAgentPid = options.parentPid;
        if (!_wfreopen(options.logFile.c_str(), L"a", stderr))
            return 1;
        setvbuf(stderr, nullptr, _IONBF, 0);
        DWORD session = 0;
        if (!ProcessIdToSessionId(GetCurrentProcessId(), &session) || session != options.consoleSession) {
            fprintf(stderr, "Desktop process must run in its designated console session.\n");
            return 1;
        }
        sasRequest = OpenEventW(EVENT_MODIFY_STATE, FALSE, options.sasEvent.c_str());
        if (!sasRequest) {
            fprintf(stderr, "Cannot open console SAS event: %lu\n", GetLastError());
            return 1;
        }
    }
    FILE *statusLog = consoleCapture ? stderr : stdout;
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
    ConsoleDesktop desktop;
    bool captureReady = (!consoleCapture || desktop.follow(statusLog)) && capture.init(consoleCapture);
    if (!captureReady && !consoleCapture) {
        fprintf(stderr, "Cannot capture current interactive user desktop.\n");
        return 2;
    }
    desktopAvailable = captureReady && !consoleCapture;
    int width = captureReady ? capture.width : 1024, height = captureReady ? capture.height : 768;
    // Do not pass application arguments to LibVNCServer's permissive argument parser.
    int rfbArgc = 1;
    char name[] = "svnc-desktop";
    char *rfbArgv[] = {name, nullptr};
    rfbScreenInfoPtr s = rfbGetScreen(&rfbArgc, rfbArgv, width, height, 8, 3, 4);
    if (!s)
        return 3;
    std::vector<char> pixels((size_t)width * height * 4);
    s->frameBuffer = pixels.data();
    serverFormat(s);
    s->desktopName = "JumpVNC Server / LibVNCServer / Windows DXGI";
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
    if (!consoleCapture && !openClipboardOwner()) {
        fprintf(stderr, "Cannot initialize clipboard owner.\n");
        rfbShutdownServer(s, TRUE);
        rfbScreenCleanup(s);
        return 6;
    }
    SetConsoleCtrlHandler(control, TRUE);
    fprintf(statusLog, "READY backend=libvncserver version=%s primary=%dx%d listen=127.0.0.1:%d view_only=%d "
                       "console_session=%lu capture=%s\n",
            LIBVNCSERVER_PACKAGE_VERSION, width, height, options.port, (int)viewOnly, options.consoleSession,
            captureReady ? (capture.gdi.pixels ? "gdi" : "dxgi") : "pending");
    ULONGLONG nextCapture = 0, nextClipboard = 0, nextRecovery = 0;
    ULONGLONG firstFrameDeadline = GetTickCount64() + 2000;
    bool tryGdi = false;
    auto loseCapture = [&] {
        captureReady = desktopAvailable = false;
        capture.reset();
        auto it = rfbGetClientIterator(s);
        while (auto cl = rfbClientIteratorNext(it))
            releaseKeys(cl);
        rfbReleaseClientIterator(it);
        std::fill(pixels.begin(), pixels.end(), 0);
        rfbMarkRectAsModified(s, 0, 0, s->width, s->height);
        fprintf(stderr, "capture.unavailable: waiting for desktop/display recovery\n");
    };
    while (running && rfbIsActive(s)) {
        if (parent && WaitForSingleObject(parent, 0) != WAIT_TIMEOUT)
            break;
        if (consoleCapture && WTSGetActiveConsoleSessionId() != options.consoleSession) {
            fprintf(stderr, "console.changed: waiting for the service to restart in the active console session\n");
            break;
        }
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
        if (consoleCapture && captureReady && !onActiveDesktop()) {
            loseCapture();
            nextRecovery = now;
            tryGdi = false;
        }
        if (!captureReady && now >= nextRecovery) {
            nextRecovery = now + 1000;
            if ((!consoleCapture || desktop.follow(statusLog)) && capture.init(consoleCapture, tryGdi)) {
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
                captureReady = true;
                desktopAvailable = !consoleCapture;
                firstFrameDeadline = now + 2000;
                fprintf(statusLog, "capture.ready primary=%dx%d method=%s\n", capture.width, capture.height,
                        capture.gdi.pixels ? "gdi" : "dxgi");
            }
            tryGdi = false;
        }
        if (captureReady && (s->clientHead || (consoleCapture && !desktopAvailable)) && now >= nextCapture) {
            nextCapture = now + (capture.gdi.pixels ? 33 : 16);
            int result = capture.update(s);
            if (result > 0) {
                desktopAvailable = true;
            } else if (result < 0 || (consoleCapture && !desktopAvailable && now >= firstFrameDeadline)) {
                tryGdi = consoleCapture && !capture.gdi.pixels;
                loseCapture();
                nextRecovery = now + 250;
            }
        }
        if (s->clientHead && (!consoleCapture || desktopAvailable))
            pollCursor(s);
        if (clipboardWindow && now >= nextClipboard && onActiveDesktop()) {
            nextClipboard = now + 100;
            pollClipboard(s);
        }
        // One owner for LibVNCServer and framebuffer memory; no concurrent writes during encoding.
        rfbProcessEvents(s, 1000);
    }
    rfbShutdownServer(s, TRUE);
    rfbScreenCleanup(s);
    closeClipboardOwner();
    if (sasRequest)
        CloseHandle(sasRequest);
    if (parent)
        CloseHandle(parent);
    return 0;
}
