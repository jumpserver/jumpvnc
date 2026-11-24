// SPDX-License-Identifier: GPL-2.0-or-later
#pragma once
#include <windows.h>
#include <rfb/keysym.h>
#include <cstdint>

// An extra bit distinguishes keypad Enter from the main Return key while
// retaining ownership and release tracking for each client's held keys.
static constexpr WORD keypadEnter = 0x100 | VK_RETURN;
inline WORD virtualKeyForKeysym(uint32_t sym) {
    WORD vk = 0;
    if (sym >= XK_F1 && sym <= XK_F24)
        vk = VK_F1 + (sym - XK_F1);
    else if (sym >= XK_KP_0 && sym <= XK_KP_9)
        vk = VK_NUMPAD0 + (sym - XK_KP_0);
    else
        switch (sym) {
        case XK_BackSpace:
            vk = VK_BACK;
            break;
        case XK_Tab:
        case XK_KP_Tab:
        case XK_ISO_Left_Tab:
            vk = VK_TAB;
            break;
        case XK_Return:
            vk = VK_RETURN;
            break;
        case XK_KP_Enter:
            vk = keypadEnter;
            break;
        case XK_Escape:
            vk = VK_ESCAPE;
            break;
        case XK_Insert:
        case XK_KP_Insert:
            vk = VK_INSERT;
            break;
        case XK_Delete:
        case XK_KP_Delete:
            vk = VK_DELETE;
            break;
        case XK_Home:
        case XK_KP_Home:
            vk = VK_HOME;
            break;
        case XK_End:
        case XK_KP_End:
            vk = VK_END;
            break;
        case XK_Page_Up:
        case XK_KP_Page_Up:
            vk = VK_PRIOR;
            break;
        case XK_Page_Down:
        case XK_KP_Page_Down:
            vk = VK_NEXT;
            break;
        case XK_Left:
        case XK_KP_Left:
            vk = VK_LEFT;
            break;
        case XK_Up:
        case XK_KP_Up:
            vk = VK_UP;
            break;
        case XK_Right:
        case XK_KP_Right:
            vk = VK_RIGHT;
            break;
        case XK_Down:
        case XK_KP_Down:
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
        case XK_Menu: vk = VK_APPS; break;
        case XK_KP_Multiply: vk = VK_MULTIPLY; break;
        case XK_KP_Add: vk = VK_ADD; break;
        case XK_KP_Separator: vk = VK_SEPARATOR; break;
        case XK_KP_Subtract: vk = VK_SUBTRACT; break;
        case XK_KP_Decimal: vk = VK_DECIMAL; break;
        case XK_KP_Divide: vk = VK_DIVIDE; break;
        default:
            if (sym >= 32 && sym < 127) {
                SHORT k = VkKeyScanW((WCHAR)sym);
                if (k != -1)
                    vk = LOBYTE(k);
            }
        }
    return vk;
}

inline uint32_t textForKeysym(uint32_t sym) {
    // X11 function/modifier keysyms are not Unicode text. Explicit Unicode
    // keysyms keep their separate 0x01000000 prefix, including private-use text.
    if (sym >= 0xfd00 && sym <= 0xffff) return 0;
    const uint32_t cp = (sym & 0xff000000) == 0x01000000 ? sym & 0xffffff : sym;
    return cp >= 32 && cp <= 0x10ffff && !(cp >= 0xd800 && cp <= 0xdfff) ? cp : 0;
}

inline INPUT keyboardInput(WORD key, bool down, bool unicode = false) {
    INPUT input{};
    input.type = INPUT_KEYBOARD;
    input.ki.dwFlags = down ? 0 : KEYEVENTF_KEYUP;
    if (unicode) {
        input.ki.wScan = key;
        input.ki.dwFlags |= KEYEVENTF_UNICODE;
        return input;
    }
    const WORD vk = key & 0xff;
    const UINT scan = vk == VK_SNAPSHOT ? 0xe037 : MapVirtualKeyW(vk, MAPVK_VK_TO_VSC_EX);
    input.ki.wScan = (WORD)(scan & 0xff);
    // VK-only input has scanCode=0 in low-level keyboard hooks. Emulators
    // commonly use those scan codes for Backspace, arrows and shortcuts.
    if (scan && (scan >> 8) != 0xe1 && vk != VK_PAUSE) {
        input.ki.dwFlags |= KEYEVENTF_SCANCODE;
        // Some layouts/drivers omit E0 from MapVirtualKey's result. Preserve
        // the navigation block and right-hand modifiers explicitly as well.
        if ((scan >> 8) == 0xe0 || key == keypadEnter || vk == VK_RCONTROL || vk == VK_RMENU ||
            (vk >= VK_PRIOR && vk <= VK_DOWN) || vk == VK_INSERT || vk == VK_DELETE ||
            vk == VK_LWIN || vk == VK_RWIN || vk == VK_APPS || vk == VK_DIVIDE)
            input.ki.dwFlags |= KEYEVENTF_EXTENDEDKEY;
    } else {
        // Pause has an E1 sequence that KEYEVENTF_EXTENDEDKEY cannot express.
        input.ki.wVk = vk;
    }
    return input;
}
