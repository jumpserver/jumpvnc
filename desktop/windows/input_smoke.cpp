// SPDX-License-Identifier: GPL-2.0-or-later
// Structural input acceptance; does not inject keys into the user's desktop.
#define WIN32_LEAN_AND_MEAN
#include "keyboard_input.h"
#include <cstdio>
#include <cstdlib>
#include <initializer_list>
static void check(bool value, const char* message) {
    if (!value) { fprintf(stderr, "%s\n", message); exit(1); }
}
int main() {
    struct Key { uint32_t sym; WORD vk, scan; bool extended; };
    const Key keys[] = {
        {XK_BackSpace, VK_BACK, 0x0e, false}, {XK_Delete, VK_DELETE, 0x53, true},
        {XK_Return, VK_RETURN, 0x1c, false}, {XK_KP_Enter, keypadEnter, 0x1c, true},
        {XK_Tab, VK_TAB, 0x0f, false}, {XK_Escape, VK_ESCAPE, 0x01, false},
        {XK_Left, VK_LEFT, 0x4b, true}, {XK_Right, VK_RIGHT, 0x4d, true},
        {XK_Up, VK_UP, 0x48, true}, {XK_Down, VK_DOWN, 0x50, true},
        {XK_Home, VK_HOME, 0x47, true}, {XK_End, VK_END, 0x4f, true},
        {XK_Page_Up, VK_PRIOR, 0x49, true}, {XK_Page_Down, VK_NEXT, 0x51, true},
        {XK_Insert, VK_INSERT, 0x52, true}, {XK_Control_L, VK_LCONTROL, 0x1d, false},
        {XK_Control_R, VK_RCONTROL, 0x1d, true}, {XK_Alt_L, VK_LMENU, 0x38, false},
        {XK_Alt_R, VK_RMENU, 0x38, true}, {XK_Shift_L, VK_LSHIFT, 0x2a, false},
        {XK_Shift_R, VK_RSHIFT, 0x36, false}, {XK_KP_0, VK_NUMPAD0, 0x52, false},
        {XK_KP_Delete, VK_DELETE, 0x53, true}, {XK_KP_Divide, VK_DIVIDE, 0x35, true},
    };
    for (const auto& key : keys) {
        check(virtualKeyForKeysym(key.sym) == key.vk, "wrong keysym mapping");
        for (bool down : {true, false}) {
            const auto input = keyboardInput(key.vk, down);
            check(input.type == INPUT_KEYBOARD && !input.ki.wVk, "expected physical keyboard input");
            check(input.ki.wScan == key.scan, "wrong scan code");
            check((input.ki.dwFlags & KEYEVENTF_SCANCODE) != 0, "missing scan-code flag");
            if (bool(input.ki.dwFlags & KEYEVENTF_EXTENDEDKEY) != key.extended) {
                fprintf(stderr, "keysym=0x%x vk=0x%x scan=0x%x flags=0x%lx expected_extended=%d\n",
                        key.sym, key.vk, input.ki.wScan, (unsigned long)input.ki.dwFlags, int(key.extended));
                check(false, "wrong extended flag");
            }
            check(bool(input.ki.dwFlags & KEYEVENTF_KEYUP) == !down, "wrong release flag");
        }
    }
    check(virtualKeyForKeysym(XK_F1) == VK_F1 && virtualKeyForKeysym(XK_F24) == VK_F24, "function key range");
    check(virtualKeyForKeysym(XK_ISO_Level3_Shift) == 0, "unsupported function keys must not become text");
    check(!textForKeysym(XK_ISO_Level3_Shift) && !textForKeysym(XK_Scroll_Lock), "control keys became Unicode text");
    check(textForKeysym(0x01004e2d) == 0x4e2d && textForKeysym(0x0101f600) == 0x1f600, "Unicode keysym decoding");
    const auto unicode = keyboardInput(0x4e2d, true, true);
    check(unicode.ki.wScan == 0x4e2d && unicode.ki.dwFlags == KEYEVENTF_UNICODE, "Unicode text changed");
    const auto pause = keyboardInput(VK_PAUSE, true);
    check(pause.ki.wVk == VK_PAUSE && !(pause.ki.dwFlags & KEYEVENTF_SCANCODE), "Pause fallback");
    printf("PASS: %u keys, down/up scan codes, modifiers, keypad Enter and Unicode\n",
           (unsigned)(sizeof(keys) / sizeof(keys[0])));
}
