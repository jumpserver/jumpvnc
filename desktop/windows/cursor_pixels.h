// SPDX-License-Identifier: GPL-2.0-or-later
#pragma once
#include <algorithm>
#include <cstddef>

inline void recoverCursorPixels(unsigned char* black, const unsigned char* white,
                                unsigned char* alpha, char* mask, size_t count) {
    for (size_t i = 0; i < count; ++i) {
        int difference = 0;
        for (int c = 0; c < 3; ++c) difference += (int)white[i * 4 + c] - black[i * 4 + c];
        const int a = std::clamp(255 - (difference + 1) / 3, 0, 255);
        alpha[i] = (unsigned char)a;
        for (int c = 0; c < 3; ++c)
            black[i * 4 + c] = a ? (unsigned char)std::min(255, (black[i * 4 + c] * 255 + a / 2) / a) : 0;
        mask[i] = a >= 128 ? 'x' : ' ';
    }
}
