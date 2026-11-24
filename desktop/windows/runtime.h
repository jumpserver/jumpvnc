// SPDX-License-Identifier: GPL-2.0-or-later
#pragma once
#include <cstdlib>
#include <cstring>
#include <cwchar>
#include "cursor_pixels.h"

struct Options {
    int port = 0;
    bool viewOnly = false;
    std::wstring passwordFile;
    DWORD parentPid = 0;
    DWORD consoleSession = 0;
    std::wstring logFile;
    std::wstring sasEvent;
};
static bool number(const wchar_t *text, unsigned long limit, unsigned long &value) {
    if (!text || !*text)
        return false;
    value = 0;
    for (const wchar_t *p = text; *p; ++p) {
        if (*p < L'0' || *p > L'9')
            return false;
        unsigned digit = *p - L'0';
        if (value > (limit - digit) / 10)
            return false;
        value = value * 10 + digit;
    }
    return value > 0;
}
static bool parseOptions(int argc, wchar_t **argv, Options &out) {
    bool listen = false, password = false, parent = false;
    for (int i = 1; i < argc; ++i) {
        if (!wcscmp(argv[i], L"--view-only")) {
            if (out.viewOnly)
                return false;
            out.viewOnly = true;
        } else if (!wcscmp(argv[i], L"--listen")) {
            if (listen || ++i == argc || wcsncmp(argv[i], L"127.0.0.1:", 10))
                return false;
            unsigned long port;
            if (!number(argv[i] + 10, 65535, port))
                return false;
            out.port = (int)port;
            listen = true;
        } else if (!wcscmp(argv[i], L"--password-file")) {
            if (password || ++i == argc || !*argv[i])
                return false;
            out.passwordFile = argv[i];
            password = true;
        } else if (!wcscmp(argv[i], L"--parent-pid")) {
            unsigned long pid;
            if (parent || ++i == argc || !number(argv[i], MAXDWORD, pid))
                return false;
            out.parentPid = (DWORD)pid;
            parent = true;
        } else if (!wcscmp(argv[i], L"--console-session")) {
            unsigned long session;
            if (out.consoleSession || ++i == argc || !number(argv[i], MAXDWORD - 1, session))
                return false;
            out.consoleSession = (DWORD)session;
        } else if (!wcscmp(argv[i], L"--log-file")) {
            if (!out.logFile.empty() || ++i == argc || !*argv[i])
                return false;
            out.logFile = argv[i];
        } else if (!wcscmp(argv[i], L"--sas-event")) {
            if (!out.sasEvent.empty() || ++i == argc || !*argv[i])
                return false;
            out.sasEvent = argv[i];
        } else
            return false;
    }
    return listen && password && (out.consoleSession ? parent && !out.logFile.empty() && !out.sasEvent.empty()
                                                   : out.logFile.empty() && out.sasEvent.empty());
}
static bool readPassword(const std::wstring &name, std::string &password) {
    HANDLE f = CreateFileW(name.c_str(), GENERIC_READ, FILE_SHARE_READ, nullptr, OPEN_EXISTING,
                           FILE_FLAG_OPEN_REPARSE_POINT, nullptr);
    if (f == INVALID_HANDLE_VALUE)
        return false;
    BY_HANDLE_FILE_INFORMATION info{};
    char data[11];
    DWORD size = 0;
    bool ok = GetFileInformationByHandle(f, &info) &&
              !(info.dwFileAttributes & (FILE_ATTRIBUTE_DIRECTORY | FILE_ATTRIBUTE_REPARSE_POINT)) &&
              info.nFileSizeHigh == 0 && info.nFileSizeLow <= 10 &&
              ReadFile(f, data, sizeof(data), &size, nullptr) && size == info.nFileSizeLow;
    CloseHandle(f);
    if (!ok)
        return false;
    password.assign(data, size);
    if (!password.empty() && password.back() == '\n') {
        password.pop_back();
        if (!password.empty() && password.back() == '\r')
            password.pop_back();
    }
    if (password.empty() || password.size() > 8)
        return false;
    for (unsigned char c : password)
        if (c < 32 || c > 126)
            return false;
    return true;
}

static constexpr size_t clipboardLimit = 1024 * 1024;
static HWND clipboardWindow = nullptr;
static DWORD clipboardSequence = 0;
static bool viewOnly = false;
static bool desktopAvailable = true;
static std::wstring pendingClipboard;
static ULONGLONG clipboardWriteDeadline = 0;
static int captureOriginX = 0, captureOriginY = 0;
static bool consoleCapture = false;
static bool getDesktopName(HDESK desktop, wchar_t (&name)[256]) {
    DWORD needed = 0;
    return desktop && GetUserObjectInformationW(desktop, UOI_NAME, name, sizeof(name), &needed);
}
static bool onActiveDesktop() {
    if (!consoleCapture)
        return true;
    wchar_t current[256]{}, active[256]{};
    HDESK input = OpenInputDesktop(0, FALSE, DESKTOP_READOBJECTS);
    bool same = getDesktopName(input, active) &&
                getDesktopName(GetThreadDesktop(GetCurrentThreadId()), current) && !_wcsicmp(current, active);
    if (input)
        CloseDesktop(input);
    return same;
}
static void receiveClipboard(char *data, int size, rfbClientPtr cl, bool utf8) {
    if (viewOnly || cl->viewOnly || !clipboardWindow || !onActiveDesktop() || size < 0 ||
        (size_t)size > clipboardLimit)
        return;
    // ExtendedClipboard text includes a terminating NUL on the wire.
    if (utf8 && size > 0 && data[size - 1] == '\0')
        --size;
    std::wstring text;
    if (utf8) {
        int n = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, data, size, nullptr, 0);
        if (size && !n)
            return;
        text.resize(n);
        if (n)
            MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, data, size, text.data(), n);
    } else {
        for (int i = 0; i < size; ++i)
            text.push_back((unsigned char)data[i]);
    }
    if (text.find(L'\0') != std::wstring::npos)
        return;
    // Windows text clipboard convention is CRLF.
    std::wstring normalized;
    for (size_t i = 0; i < text.size(); ++i) {
        if (text[i] == L'\n' && (i == 0 || text[i - 1] != L'\r'))
            normalized += L'\r';
        normalized += text[i];
    }
    pendingClipboard = std::move(normalized);
    clipboardWriteDeadline = GetTickCount64() + 2000;
}
static void applyClipboard() {
    if (!clipboardWriteDeadline)
        return;
    if (GetTickCount64() > clipboardWriteDeadline) {
        clipboardWriteDeadline = 0;
        pendingClipboard.clear();
        fprintf(stderr, "clipboard.write_timeout\n");
        return;
    }
    HGLOBAL block = GlobalAlloc(GMEM_MOVEABLE, (pendingClipboard.size() + 1) * sizeof(wchar_t));
    if (!block)
        return;
    void *memory = GlobalLock(block);
    if (!memory) {
        GlobalFree(block);
        return;
    }
    memcpy(memory, pendingClipboard.c_str(), (pendingClipboard.size() + 1) * sizeof(wchar_t));
    GlobalUnlock(block);
    if (!OpenClipboard(clipboardWindow)) {
        GlobalFree(block);
        return;
    }
    bool ok = EmptyClipboard() && SetClipboardData(CF_UNICODETEXT, block);
    if (!ok) {
        fprintf(stderr, "clipboard.write_failed win32=%lu\n", GetLastError());
        GlobalFree(block);
    }
    CloseClipboard();
    if (ok) {
        clipboardSequence = GetClipboardSequenceNumber();
        clipboardWriteDeadline = 0;
        pendingClipboard.clear();
    }
}

static void cutText(char *data, int size, rfbClientPtr cl) { receiveClipboard(data, size, cl, false); }
static void cutTextUTF8(char *data, int size, rfbClientPtr cl) { receiveClipboard(data, size, cl, true); }
static void pollClipboard(rfbScreenInfoPtr screen) {
    applyClipboard();
    if (clipboardWriteDeadline)
        return;
    // LibVNCServer 0.9.15 broadcasts to every client, including handshaking
    // sockets. Defer until all remaining clients are authenticated. The main
    // loop reclaims incomplete handshakes after ten seconds.
    auto iterator = rfbGetClientIterator(screen);
    bool authenticated = true, hasClients = false;
    while (auto cl = rfbClientIteratorNext(iterator)) {
        hasClients = true;
        if (cl->state != rfbClientRec::RFB_NORMAL)
            authenticated = false;
    }
    rfbReleaseClientIterator(iterator);
    if (!hasClients || !authenticated)
        return;
    DWORD sequence = GetClipboardSequenceNumber();
    if (!sequence || sequence == clipboardSequence || !screen->clientHead)
        return;
    if (!OpenClipboard(clipboardWindow))
        return;
    std::wstring text;
    bool validText = false;
    HANDLE block = GetClipboardData(CF_UNICODETEXT);
    if (block && GlobalSize(block) <= (clipboardLimit + 1) * sizeof(wchar_t)) {
        const wchar_t *data = (const wchar_t *)GlobalLock(block);
        if (data) {
            size_t count = GlobalSize(block) / sizeof(wchar_t), n = 0;
            while (n < count && data[n])
                ++n;
            if (n < count) {
                text.assign(data, n);
                validText = true;
            }
            GlobalUnlock(block);
        }
    }
    // Do not translate a clipboard containing only files/images into empty text.
    bool hasText = IsClipboardFormatAvailable(CF_UNICODETEXT) != FALSE;
    CloseClipboard();
    clipboardSequence = sequence;
    if (!hasText || !validText || text.size() > clipboardLimit)
        return;
    int n = WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, text.data(), (int)text.size(), nullptr, 0,
                                nullptr, nullptr);
    if ((n == 0 && !text.empty()) || n > (int)clipboardLimit)
        return;
    std::string utf8(n, '\0'), latin;
    if (n)
        WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, text.data(), (int)text.size(), utf8.data(), n,
                            nullptr, nullptr);
    for (wchar_t c : text)
        latin += c <= 255 ? (char)c : '?';
    rfbSendServerCutTextUTF8(screen, utf8.data(), (int)utf8.size(), latin.data(), (int)latin.size());
}

// Convert the real Windows cursor to a rich RFB cursor. Drawing on black and
// white backgrounds distinguishes transparent pixels, including legacy masks.
static void pollCursor(rfbScreenInfoPtr screen) {
    CURSORINFO info{sizeof(info)};
    if (!GetCursorInfo(&info))
        return;
    bool visible = (info.flags & CURSOR_SHOWING) && info.ptScreenPos.x >= captureOriginX &&
                   info.ptScreenPos.y >= captureOriginY &&
                   info.ptScreenPos.x < captureOriginX + screen->width &&
                   info.ptScreenPos.y < captureOriginY + screen->height;
    HCURSOR handle = visible ? info.hCursor : nullptr;
    static HCURSOR previous = nullptr;
    static bool initialized = false;
    int x = std::clamp((int)info.ptScreenPos.x - captureOriginX, 0, screen->width - 1);
    int y = std::clamp((int)info.ptScreenPos.y - captureOriginY, 0, screen->height - 1);
    if (screen->cursorX != x || screen->cursorY != y) {
        screen->cursorX = x;
        screen->cursorY = y;
        auto it = rfbGetClientIterator(screen);
        while (auto cl = rfbClientIteratorNext(it))
            cl->cursorWasMoved = TRUE;
        rfbReleaseClientIterator(it);
    }
    if (initialized && handle == previous)
        return;
    if (!visible) {
        char empty[] = " ";
        rfbSetCursor(screen, rfbMakeXCursor(1, 1, empty, empty));
    } else {
        ICONINFO icon{};
        if (!GetIconInfo(handle, &icon))
            return;
        BITMAP bitmap{};
        GetObject(icon.hbmColor ? icon.hbmColor : icon.hbmMask, sizeof(bitmap), &bitmap);
        int w = bitmap.bmWidth, h = icon.hbmColor ? bitmap.bmHeight : bitmap.bmHeight / 2;
        if (icon.hbmColor)
            DeleteObject(icon.hbmColor);
        if (icon.hbmMask)
            DeleteObject(icon.hbmMask);
        if (w < 1 || h < 1 || w > 256 || h > 256)
            return;
        BITMAPINFO bi{};
        bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
        bi.bmiHeader.biWidth = w;
        bi.bmiHeader.biHeight = -h;
        bi.bmiHeader.biPlanes = 1;
        bi.bmiHeader.biBitCount = 32;
        bi.bmiHeader.biCompression = BI_RGB;
        HDC dc = CreateCompatibleDC(nullptr);
        void *bits = nullptr;
        HBITMAP dib = CreateDIBSection(dc, &bi, DIB_RGB_COLORS, &bits, nullptr, 0);
        if (!dc || !dib) {
            if (dib)
                DeleteObject(dib);
            if (dc)
                DeleteDC(dc);
            return;
        }
        auto old = SelectObject(dc, dib);
        memset(bits, 0, (size_t)w * h * 4);
        DrawIconEx(dc, 0, 0, handle, w, h, 0, nullptr, DI_NORMAL);
        std::vector<unsigned char> pixels((unsigned char *)bits, (unsigned char *)bits + (size_t)w * h * 4);
        memset(bits, 255, (size_t)w * h * 4);
        DrawIconEx(dc, 0, 0, handle, w, h, 0, nullptr, DI_NORMAL);
        std::vector<char> source(w * h, ' '), mask(w * h, ' ');
        std::vector<unsigned char> alpha(w * h);
        auto white = (unsigned char *)bits;
        recoverCursorPixels(pixels.data(), white, alpha.data(), mask.data(), (size_t)w * h);
        auto cursor = rfbMakeXCursor(w, h, source.data(), mask.data());
        if (cursor) {
            cursor->richSource = (unsigned char *)malloc(pixels.size());
            cursor->alphaSource = (unsigned char *)malloc(alpha.size());
            cursor->cleanupRichSource = TRUE;
            if (cursor->richSource && cursor->alphaSource) {
                memcpy(cursor->richSource, pixels.data(), pixels.size());
                memcpy(cursor->alphaSource, alpha.data(), alpha.size());
                cursor->alphaPreMultiplied = FALSE;
                cursor->xhot = (unsigned short)std::min(icon.xHotspot, (DWORD)w - 1);
                cursor->yhot = (unsigned short)std::min(icon.yHotspot, (DWORD)h - 1);
                rfbSetCursor(screen, cursor);
            } else
                rfbFreeCursor(cursor);
        }
        SelectObject(dc, old);
        DeleteObject(dib);
        DeleteDC(dc);
    }
    previous = handle;
    initialized = true;
}
