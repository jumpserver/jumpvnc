// SPDX-License-Identifier: GPL-2.0-or-later
#pragma once
#include <rfb/rfb.h>
#ifdef __cplusplus
extern "C" {
#endif
/* Encoding 50 is the noVNC-compatible H.264 encoding. LibVNCServer's
 * rfbEncodingH264 (0x48323634) is a different wire format and is not used. */
rfbBool svncH264Select(rfbClientPtr client);
void svncH264Reset(rfbClientPtr client);
void svncH264Offered(rfbClientPtr client, unsigned encoding);
rfbBool svncH264Send(rfbClientPtr client);
#ifdef __cplusplus
}
#endif
