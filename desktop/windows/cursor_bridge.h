// SPDX-License-Identifier: GPL-2.0-or-later
#pragma once
#include <rfb/rfb.h>
#ifdef __cplusplus
extern "C" {
#endif
void svncCursorReset(rfbClientPtr client);
void svncCursorEnable(rfbClientPtr client);
rfbBool svncSendCursorShape(rfbClientPtr client);
#ifdef __cplusplus
}
#endif
