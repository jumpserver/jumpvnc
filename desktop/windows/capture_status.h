// SPDX-License-Identifier: GPL-2.0-or-later
#pragma once
#include <rfb/rfb.h>
#ifdef __cplusplus
extern "C" {
#endif
/* Negotiated JVCP pseudo-rectangle: zero geometry, one byte of capture state. */
#define SVNC_CAPTURE_STATUS 0x4a564350
void svncCaptureEnable(rfbClientPtr client, rfbBool enabled);
int svncPendingCapture(rfbClientPtr client);
void svncAppendCapture(rfbClientPtr client, int method);
#ifdef __cplusplus
}
#endif
