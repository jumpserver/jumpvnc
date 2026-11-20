// SPDX-License-Identifier: GPL-2.0-or-later
// X11 desktop adapter. SSH, authorization, audit and file transfer belong to the Go Agent.
#include <X11/Xatom.h>
#include <X11/XKBlib.h>
#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/extensions/XTest.h>
#include <X11/extensions/Xfixes.h>
#include <X11/keysym.h>
#include <rfb/rfb.h>
#include <algorithm>
#include <arpa/inet.h>
#include <chrono>
#include <climits>
#include <csignal>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fcntl.h>
#include <map>
#include <string>
#include <sys/prctl.h>
#include <sys/stat.h>
#include <unistd.h>
#include <vector>

#if !defined(LIBVNCSERVER_HAVE_LIBJPEG) || !defined(LIBVNCSERVER_HAVE_LIBZ)
#error "LibVNCServer must support JPEG and zlib (Tight encoding / ExtendedClipboard)"
#endif

static uint64_t milliseconds() {
    return std::chrono::duration_cast<std::chrono::milliseconds>(
               std::chrono::steady_clock::now().time_since_epoch()).count();
}
static volatile sig_atomic_t running = 1;
static void stop(int) { running = 0; }
static Display *display;
static Window root;
static bool viewOnly;
static bool desktopAvailable = true;
static int xerror;
static int handleXError(Display *, XErrorEvent *e) {
    xerror = e->error_code;
    return 0; // A disappearing window/selection is recoverable; X connection loss remains fatal.
}
struct Options {
    int port = 0;
    pid_t parent = 0;
    std::string passwordFile;
};
static bool number(const char *text, unsigned limit, unsigned &value) {
    value = 0;
    if (!text || !*text) return false;
    for (; *text; ++text) {
        if (*text < '0' || *text > '9') return false;
        unsigned digit = *text - '0';
        if (value > (limit - digit) / 10) return false;
        value = value * 10 + digit;
    }
    return value != 0;
}
static bool options(int argc, char **argv, Options &out) {
    for (int i = 1; i < argc; ++i) {
        if (!strcmp(argv[i], "--view-only") && !viewOnly) viewOnly = true;
        else if (!strcmp(argv[i], "--listen") && !out.port && ++i < argc) {
            unsigned port;
            if (strncmp(argv[i], "127.0.0.1:", 10) || !number(argv[i] + 10, 65535, port)) return false;
            out.port = static_cast<int>(port);
        } else if (!strcmp(argv[i], "--password-file") && out.passwordFile.empty() && ++i < argc)
            out.passwordFile = argv[i];
        else if (!strcmp(argv[i], "--parent-pid") && !out.parent && ++i < argc) {
            unsigned pid;
            if (!number(argv[i], INT_MAX, pid)) return false;
            out.parent = static_cast<pid_t>(pid);
        } else return false;
    }
    return out.port && !out.passwordFile.empty();
}
static bool readPassword(const std::string &name, std::string &password) {
    int fd = open(name.c_str(), O_RDONLY | O_CLOEXEC | O_NOFOLLOW | O_NONBLOCK);
    if (fd < 0) return false;
    struct stat st{};
    char data[11];
    bool valid = !fstat(fd, &st) && S_ISREG(st.st_mode) && !(st.st_mode & 0077) &&
                 st.st_size >= 1 && st.st_size <= 10;
    ssize_t size = valid ? read(fd, data, sizeof(data)) : -1;
    close(fd);
    if (!valid || size != st.st_size) return false;
    password.assign(data, size);
    if (!password.empty() && password.back() == '\n') {
        password.pop_back();
        if (!password.empty() && password.back() == '\r') password.pop_back();
    }
    if (password.empty() || password.size() > 8) return false;
    for (unsigned char c : password) if (c < 32 || c > 126) return false;
    return true;
}

struct ClientInput {
    std::map<rfbKeySym, KeyCode> keys;
    unsigned buttons = 0;
    uint64_t created = milliseconds();
};
static std::map<KeyCode, unsigned> keyOwners;
static unsigned buttonOwners[7]{};
static unsigned clientCount;
static void releaseKeys(rfbClientPtr cl) {
    if (!cl->clientData) return;
    auto &state = *static_cast<ClientInput *>(cl->clientData);
    for (auto entry : state.keys) {
        if (--keyOwners[entry.second] == 0) {
            XTestFakeKeyEvent(display, entry.second, False, CurrentTime);
            keyOwners.erase(entry.second);
        }
    }
    state.keys.clear();
    for (unsigned b = 0; b < 7; ++b)
        if ((state.buttons & (1u << b)) && buttonOwners[b] && --buttonOwners[b] == 0)
            XTestFakeButtonEvent(display, b + 1, False, CurrentTime);
    state.buttons = 0;
    XFlush(display);
}
static void disconnected(rfbClientPtr cl) {
    releaseKeys(cl);
    delete static_cast<ClientInput *>(cl->clientData);
    cl->clientData = nullptr;
    if (clientCount) --clientCount;
}
static rfbNewClientAction connected(rfbClientPtr cl) {
    if (clientCount >= 16) return RFB_CLIENT_REFUSE;
    cl->clientData = new ClientInput;
    cl->clientGoneHook = disconnected;
    cl->viewOnly = viewOnly ? TRUE : FALSE;
    ++clientCount;
    return RFB_CLIENT_ACCEPT;
}
static void keyboard(rfbBool down, rfbKeySym sym, rfbClientPtr cl) {
    if (!desktopAvailable || viewOnly || cl->viewOnly || !cl->clientData) return;
    auto &state = *static_cast<ClientInput *>(cl->clientData);
    auto held = state.keys.find(sym);
    if (!down) {
        if (held != state.keys.end()) {
            KeyCode code = held->second;
            state.keys.erase(held);
            if (--keyOwners[code] == 0) {
                XTestFakeKeyEvent(display, code, False, CurrentTime);
                keyOwners.erase(code);
            }
        }
    } else {
        KeyCode code = held == state.keys.end() ? XKeysymToKeycode(display, sym) : held->second;
        if (!code) return; // Unmapped Unicode characters can be pasted through UTF-8 clipboard.
        if (held == state.keys.end()) { state.keys[sym] = code; ++keyOwners[code]; }
        XkbStateRec kb{};
        XkbGetState(display, XkbUseCoreKbd, &kb);
        bool shifted = XkbKeycodeToKeysym(display, code, kb.group, 0) != sym &&
                       XkbKeycodeToKeysym(display, code, kb.group, 1) == sym && !(kb.mods & ShiftMask);
        KeyCode shift = XKeysymToKeycode(display, XK_Shift_L);
        if (shifted && shift) XTestFakeKeyEvent(display, shift, True, CurrentTime);
        XTestFakeKeyEvent(display, code, True, CurrentTime);
        if (shifted && shift) XTestFakeKeyEvent(display, shift, False, CurrentTime);
    }
    XFlush(display);
}
static void pointer(int mask, int x, int y, rfbClientPtr cl) {
    if (!desktopAvailable || viewOnly || cl->viewOnly || !cl->clientData || x < 0 || y < 0 ||
        x >= cl->screen->width || y >= cl->screen->height) return;
    auto &state = *static_cast<ClientInput *>(cl->clientData);
    XTestFakeMotionEvent(display, DefaultScreen(display), x, y, CurrentTime);
    for (unsigned b = 0; b < 7; ++b) {
        if ((static_cast<unsigned>(mask) ^ state.buttons) & (1u << b)) {
            bool down = mask & (1u << b);
            bool emit = down ? buttonOwners[b]++ == 0 : buttonOwners[b] && --buttonOwners[b] == 0;
            if (emit) XTestFakeButtonEvent(display, b + 1, down, CurrentTime);
        }
    }
    state.buttons = static_cast<unsigned>(mask) & 127u;
    XFlush(display);
    rfbDefaultPtrAddEvent(mask, x, y, cl);
}

// Deliberately bounded to a single X property. INCR / large clipboard transfers are not supported.
static constexpr size_t clipboardLimit = 64 * 1024;
struct Clipboard {
    Window window = 0;
    Atom selection, utf8, targets, property;
    int fixesEvent = 0;
    bool pending = false, dirty = false, triedLatin = false;
    uint64_t deadline = 0;
    std::string text;
    bool init() {
        int error;
        if (!XFixesQueryExtension(display, &fixesEvent, &error)) return false;
        selection = XInternAtom(display, "CLIPBOARD", False);
        utf8 = XInternAtom(display, "UTF8_STRING", False);
        targets = XInternAtom(display, "TARGETS", False);
        property = XInternAtom(display, "SVNC_CLIPBOARD", False);
        window = XCreateSimpleWindow(display, root, -1, -1, 1, 1, 0, 0, 0);
        XFixesSelectSelectionInput(display, window, selection,
            XFixesSetSelectionOwnerNotifyMask | XFixesSelectionWindowDestroyNotifyMask |
            XFixesSelectionClientCloseNotifyMask);
        fetch();
        return window != 0;
    }
    void fetch(bool latin = false) {
        dirty = false;
        Window owner = XGetSelectionOwner(display, selection);
        if (owner == None || owner == window) { pending = false; return; }
        triedLatin = latin;
        pending = true;
        deadline = milliseconds() + 2000;
        XDeleteProperty(display, window, property);
        XConvertSelection(display, selection, latin ? XA_STRING : utf8, property, window, CurrentTime);
        XFlush(display);
    }
    static std::string toUTF8(const char *data, size_t size) {
        std::string result;
        for (size_t i = 0; i < size; ++i) {
            unsigned char c = data[i];
            if (c < 128) result += static_cast<char>(c);
            else { result += static_cast<char>(0xc0 | (c >> 6)); result += static_cast<char>(0x80 | (c & 63)); }
        }
        return result;
    }
    static bool validUTF8(const std::string &text) {
        for (size_t i = 0; i < text.size();) {
            unsigned char c = text[i++];
            if (!c) return false;
            if (c < 128) continue;
            unsigned cp, count, minimum;
            if (c >= 0xc2 && c <= 0xdf) { cp = c & 31; count = 1; minimum = 128; }
            else if (c >= 0xe0 && c <= 0xef) { cp = c & 15; count = 2; minimum = 2048; }
            else if (c >= 0xf0 && c <= 0xf4) { cp = c & 7; count = 3; minimum = 65536; }
            else return false;
            if (i + count > text.size()) return false;
            while (count--) {
                unsigned char next = text[i++];
                if ((next & 0xc0) != 0x80) return false;
                cp = (cp << 6) | (next & 63);
            }
            if (cp < minimum || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff)) return false;
        }
        return true;
    }
    static std::string latin1(const std::string &text) {
        std::string result;
        for (size_t i = 0; i < text.size();) {
            unsigned char c = text[i++];
            if (c < 128) result += static_cast<char>(c);
            else if (c < 0xc4 && i < text.size()) {
                result += static_cast<char>(((c & 31) << 6) | (static_cast<unsigned char>(text[i++]) & 63));
            } else {
                result += '?';
                while (i < text.size() && (static_cast<unsigned char>(text[i]) & 0xc0) == 0x80) ++i;
            }
        }
        return result;
    }
    void receive(char *data, int size, rfbClientPtr cl, bool unicode) {
        if (viewOnly || cl->viewOnly || size < 0 || static_cast<size_t>(size) > clipboardLimit) return;
        if (unicode && size && data[size - 1] == '\0') --size;
        std::string received = unicode ? std::string(data, size) : toUTF8(data, size);
        if (received.size() > clipboardLimit || !validUTF8(received)) return;
        // RFB viewers can normalize wire text to CRLF; X11 text convention is LF.
        text.clear();
        for (size_t i = 0; i < received.size(); ++i) {
            if (received[i] == '\r' && i + 1 < received.size() && received[i + 1] == '\n') continue;
            text += received[i];
        }
        pending = false;
        dirty = false; // Do not echo a viewer's own clipboard back to it.
        XSetSelectionOwner(display, selection, window, CurrentTime);
        XFlush(display);
    }
    void event(const XEvent &event) {
        if (event.type == fixesEvent + XFixesSelectionNotify) {
            auto &selectionEvent = *reinterpret_cast<const XFixesSelectionNotifyEvent *>(&event);
            if (selectionEvent.owner != window) fetch();
        } else if (event.type == SelectionRequest) {
            const auto &request = event.xselectionrequest;
            XEvent reply{};
            reply.xselection = {SelectionNotify, 0, True, display, request.requestor,
                                request.selection, request.target, None, request.time};
            Atom destination = request.property == None ? request.target : request.property;
            if (request.selection == selection && XGetSelectionOwner(display, selection) == window) {
                if (request.target == targets) {
                    Atom supported[] = {targets, utf8, XA_STRING};
                    XChangeProperty(display, request.requestor, destination, XA_ATOM, 32,
                                    PropModeReplace, reinterpret_cast<unsigned char *>(supported), 3);
                    reply.xselection.property = destination;
                } else if (request.target == utf8 || request.target == XA_STRING) {
                    std::string content = request.target == utf8 ? text : latin1(text);
                    XChangeProperty(display, request.requestor, destination, request.target, 8,
                        PropModeReplace, reinterpret_cast<const unsigned char *>(content.data()), content.size());
                    reply.xselection.property = destination;
                }
            }
            XSendEvent(display, request.requestor, False, 0, &reply);
            XFlush(display);
        } else if (event.type == SelectionNotify && pending &&
                   event.xselection.requestor == window && event.xselection.selection == selection) {
            if (event.xselection.property == None) {
                pending = false;
                if (!triedLatin) fetch(true);
                return;
            }
            Atom type;
            int format;
            unsigned long count, remaining;
            unsigned char *data = nullptr;
            int status = XGetWindowProperty(display, window, property, 0, clipboardLimit / 4,
                                           True, AnyPropertyType, &type, &format, &count, &remaining, &data);
            pending = false;
            if (status == Success && format == 8 && !remaining && (type == utf8 || type == XA_STRING)) {
                std::string received = type == utf8 ? std::string(reinterpret_cast<char *>(data), count) :
                    toUTF8(reinterpret_cast<char *>(data), count);
                if (received.size() <= clipboardLimit && validUTF8(received)) {
                    text = std::move(received);
                    dirty = true;
                }
            }
            if (data) XFree(data);
            XDeleteProperty(display, window, property);
        }
    }
    void poll(rfbScreenInfoPtr screen) {
        if (pending && milliseconds() > deadline) pending = false;
        if (!dirty) return;
        // The library broadcast includes handshaking sockets. Delay until all clients authenticate.
        bool authenticated = true, hasClients = false;
        auto it = rfbGetClientIterator(screen);
        while (auto cl = rfbClientIteratorNext(it)) {
            hasClients = true;
            if (cl->state != rfbClientRec::RFB_NORMAL) authenticated = false;
        }
        rfbReleaseClientIterator(it);
        if (!hasClients || !authenticated) return;
        std::string latin = latin1(text);
        rfbSendServerCutTextUTF8(screen, text.data(), text.size(), latin.data(), latin.size());
        dirty = false;
    }
};
static Clipboard clipboard;
static void cutText(char *data, int size, rfbClientPtr cl) { clipboard.receive(data, size, cl, false); }
static void cutTextUTF8(char *data, int size, rfbClientPtr cl) { clipboard.receive(data, size, cl, true); }

static void serverFormat(rfbScreenInfoPtr screen) {
    screen->serverFormat.redShift = 16;
    screen->serverFormat.greenShift = 8;
    screen->serverFormat.blueShift = 0;
    screen->serverFormat.redMax = screen->serverFormat.greenMax = screen->serverFormat.blueMax = 255;
    screen->serverFormat.depth = 24;
    screen->serverFormat.bigEndian = FALSE;
    auto it = rfbGetClientIterator(screen);
    while (auto cl = rfbClientIteratorNext(it)) screen->setTranslateFunction(cl);
    rfbReleaseClientIterator(it);
}
static bool dimensions(int &width, int &height) {
    XWindowAttributes attributes{};
    if (!XGetWindowAttributes(display, root, &attributes) || attributes.visual->c_class != TrueColor) return false;
    width = attributes.width;
    height = attributes.height;
    return width > 0 && height > 0 && width <= 16384 && height <= 16384 &&
           static_cast<size_t>(width) * height <= 64 * 1024 * 1024;
}
static bool capture(rfbScreenInfoPtr screen, std::vector<char> &pixels) {
    int width, height;
    if (!dimensions(width, height)) return false;
    if (width != screen->width || height != screen->height) {
        auto it = rfbGetClientIterator(screen);
        while (auto cl = rfbClientIteratorNext(it)) if (!cl->useNewFBSize) rfbCloseClient(cl);
        rfbReleaseClientIterator(it);
        std::vector<char> resized(static_cast<size_t>(width) * height * 4);
        rfbNewFramebuffer(screen, resized.data(), width, height, 8, 3, 4);
        pixels.swap(resized);
        serverFormat(screen);
    }
    XSync(display, False);
    xerror = 0;
    XImage *image = XGetImage(display, root, 0, 0, width, height, AllPlanes, ZPixmap);
    if (!image || xerror) { if (image) XDestroyImage(image); return false; }
    bool direct = image->bits_per_pixel == 32 && image->byte_order == LSBFirst &&
                  image->red_mask == 0xff0000 && image->green_mask == 0xff00 && image->blue_mask == 0xff;
    auto component = [](unsigned long pixel, unsigned long mask) {
        if (!mask) return 0ul;
        while (!(mask & 1)) { mask >>= 1; pixel >>= 1; }
        return ((pixel & mask) * 255 / mask);
    };
    std::vector<char> converted;
    if (!direct) {
        converted.resize(static_cast<size_t>(width) * height * 4);
        for (int y = 0; y < height; ++y) for (int x = 0; x < width; ++x) {
            unsigned long pixel = XGetPixel(image, x, y);
            char *out = converted.data() + (static_cast<size_t>(y) * width + x) * 4;
            out[0] = component(pixel, image->blue_mask);
            out[1] = component(pixel, image->green_mask);
            out[2] = component(pixel, image->red_mask);
            out[3] = 0;
        }
    }
    const char *data = direct ? image->data : converted.data();
    size_t stride = direct ? image->bytes_per_line : static_cast<size_t>(width) * 4;
    for (int y = 0; y < height; y += 64) {
        int bottom = std::min(y + 64, height), run = -1;
        for (int x = 0; x < width; x += 64) {
            int right = std::min(x + 64, width);
            bool changed = false;
            for (int row = y; row < bottom && !changed; ++row)
                changed = memcmp(pixels.data() + (static_cast<size_t>(row) * width + x) * 4,
                                 data + row * stride + x * 4, (right - x) * 4) != 0;
            if (changed) {
                for (int row = y; row < bottom; ++row)
                    memcpy(pixels.data() + (static_cast<size_t>(row) * width + x) * 4,
                           data + row * stride + x * 4, (right - x) * 4);
                if (run < 0) run = x;
            } else if (run >= 0) { rfbMarkRectAsModified(screen, run, y, x, bottom); run = -1; }
        }
        if (run >= 0) rfbMarkRectAsModified(screen, run, y, width, bottom);
    }
    XDestroyImage(image);
    return true;
}
static void cursor(rfbScreenInfoPtr screen) {
    XFixesCursorImage *image = XFixesGetCursorImage(display);
    if (!image) return;
    int x = std::clamp(static_cast<int>(image->x), 0, screen->width - 1);
    int y = std::clamp(static_cast<int>(image->y), 0, screen->height - 1);
    if (screen->cursorX != x || screen->cursorY != y) {
        screen->cursorX = x;
        screen->cursorY = y;
        auto it = rfbGetClientIterator(screen);
        while (auto cl = rfbClientIteratorNext(it)) cl->cursorWasMoved = TRUE;
        rfbReleaseClientIterator(it);
    }
    static unsigned long serial = ~0ul;
    if (serial != image->cursor_serial && image->width && image->height &&
        image->width <= 256 && image->height <= 256) {
        size_t count = static_cast<size_t>(image->width) * image->height;
        std::vector<char> source(count, ' '), mask(count, ' ');
        for (size_t i = 0; i < count; ++i) if (image->pixels[i] >> 24 & 255) mask[i] = 'x';
        auto shape = rfbMakeXCursor(image->width, image->height, source.data(), mask.data());
        if (shape) {
            shape->richSource = static_cast<unsigned char *>(malloc(count * 4));
            if (shape->richSource) {
                for (size_t i = 0; i < count; ++i) {
                    unsigned long p = image->pixels[i], alpha = (p >> 24) & 255;
                    for (int c = 0; c < 3; ++c)
                        shape->richSource[i * 4 + c] = alpha ? std::min(255ul, ((p >> (8 * c)) & 255) * 255 / alpha) : 0;
                    shape->richSource[i * 4 + 3] = 0;
                }
                shape->cleanupRichSource = TRUE;
                shape->xhot = std::min(image->xhot, static_cast<unsigned short>(image->width - 1));
                shape->yhot = std::min(image->yhot, static_cast<unsigned short>(image->height - 1));
                rfbSetCursor(screen, shape);
                serial = image->cursor_serial;
            } else rfbFreeCursor(shape);
        }
    }
    XFree(image);
}
int main(int argc, char **argv) {
    setvbuf(stdout, nullptr, _IONBF, 0);
    setvbuf(stderr, nullptr, _IONBF, 0);
    Options opts;
    if (!options(argc, argv, opts)) {
        fprintf(stderr, "Usage: svnc-desktop --listen 127.0.0.1:PORT --password-file PATH [--view-only] [--parent-pid PID]\n");
        return 1;
    }
    struct sigaction action{};
    action.sa_handler = stop;
    sigemptyset(&action.sa_mask);
    sigaction(SIGTERM, &action, nullptr);
    sigaction(SIGINT, &action, nullptr);
    signal(SIGPIPE, SIG_IGN);
    if (opts.parent && (prctl(PR_SET_PDEATHSIG, SIGTERM) || getppid() != opts.parent)) {
        fprintf(stderr, "Cannot monitor Agent parent process.\n");
        return 1;
    }
    const char *session = getenv("XDG_SESSION_TYPE");
    if ((session && !strcmp(session, "wayland")) || getenv("WAYLAND_DISPLAY")) {
        fprintf(stderr, "This backend requires an X11 session; XWayland cannot share the Wayland desktop.\n");
        return 2;
    }
    std::string password;
    if (!readPassword(opts.passwordFile, password)) {
        fprintf(stderr, "Password must be a private regular file containing 1-8 printable ASCII bytes.\n");
        return 1;
    }
    display = XOpenDisplay(nullptr);
    if (!display) { fprintf(stderr, "Cannot open X11 DISPLAY; run as the logged-in desktop user with XAUTHORITY.\n"); return 2; }
    XSetErrorHandler(handleXError);
    root = DefaultRootWindow(display);
    int event, error, major, minor, width, height;
    if (!XTestQueryExtension(display, &event, &error, &major, &minor) ||
        !dimensions(width, height) || !clipboard.init()) {
        fprintf(stderr, "A TrueColor X11 desktop with XTEST and XFIXES is required.\n");
        XCloseDisplay(display);
        return 2;
    }
    int rfbArgc = 1;
    char name[] = "svnc-desktop";
    char *rfbArgv[] = {name, nullptr};
    auto screen = rfbGetScreen(&rfbArgc, rfbArgv, width, height, 8, 3, 4);
    if (!screen) { XCloseDisplay(display); return 3; }
    std::vector<char> pixels(static_cast<size_t>(width) * height * 4);
    screen->frameBuffer = pixels.data();
    serverFormat(screen);
    screen->desktopName = "SuperVNC Server / LibVNCServer / Linux X11";
    screen->listenInterface = htonl(INADDR_LOOPBACK);
    screen->port = opts.port;
    screen->autoPort = FALSE;
#ifdef LIBVNCSERVER_IPv6
    screen->ipv6port = -1;
#endif
    screen->httpPort = -1;
    screen->httpDir = nullptr;
    screen->alwaysShared = TRUE;
    screen->permitFileTransfer = FALSE;
    // Distribution libraries may include optional TightVNC file-transfer extensions.
    // This process needs only core RFB; disable registered protocol extensions as well.
    auto extensions = rfbGetExtensionIterator();
    rfbReleaseExtensionIterator();
    rfbUnregisterProtocolExtension(extensions);
    char *passwords[] = {password.data(), nullptr};
    screen->authPasswdData = passwords;
    screen->passwordCheck = rfbCheckPasswordByList;
    screen->kbdAddEvent = keyboard;
    screen->kbdReleaseAllKeys = releaseKeys;
    screen->ptrAddEvent = pointer;
    screen->newClientHook = connected;
    screen->setXCutText = cutText;
    screen->setXCutTextUTF8 = cutTextUTF8;
    screen->deferUpdateTime = 5;
    screen->deferPtrUpdateTime = 0;
    screen->handleEventsEagerly = TRUE;
    screen->maxClientWait = 2000;
    rfbInitServer(screen);
    if (screen->listenSock == RFB_INVALID_SOCKET) {
        rfbScreenCleanup(screen);
        XCloseDisplay(display);
        return 4;
    }
    printf("READY backend=libvncserver version=%s desktop=%dx%d listen=127.0.0.1:%d view_only=%d file_transfer=sftp\n",
        LIBVNCSERVER_PACKAGE_VERSION, width, height, opts.port, static_cast<int>(viewOnly));
    uint64_t nextCapture = 0, nextCursor = 0;
    bool captureReady = true;
    while (running && rfbIsActive(screen)) {
        while (XPending(display)) { XEvent e; XNextEvent(display, &e); clipboard.event(e); }
        uint64_t now = milliseconds();
        auto it = rfbGetClientIterator(screen);
        while (auto cl = rfbClientIteratorNext(it)) {
            auto state = static_cast<ClientInput *>(cl->clientData);
            if (state && cl->state != rfbClientRec::RFB_NORMAL && now - state->created > 10000) rfbCloseClient(cl);
        }
        rfbReleaseClientIterator(it);
        if (screen->clientHead && now >= nextCapture) {
            nextCapture = now + (captureReady ? 33 : 250);
            bool available = capture(screen, pixels);
            if (!available && captureReady) {
                it = rfbGetClientIterator(screen);
                while (auto cl = rfbClientIteratorNext(it)) releaseKeys(cl);
                rfbReleaseClientIterator(it);
                std::fill(pixels.begin(), pixels.end(), 0);
                rfbMarkRectAsModified(screen, 0, 0, screen->width, screen->height);
                fprintf(stderr, "capture.unavailable: waiting for X11 display recovery\n");
            }
            captureReady = available;
            desktopAvailable = available;
        }
        if (screen->clientHead && now >= nextCursor) { nextCursor = now + 33; cursor(screen); }
        clipboard.poll(screen);
        rfbProcessEvents(screen, 1000); // A single owner for Xlib, LibVNCServer and framebuffer memory.
    }
    rfbShutdownServer(screen, TRUE);
    rfbScreenCleanup(screen);
    XDestroyWindow(display, clipboard.window);
    XCloseDisplay(display);
    return 0;
}
