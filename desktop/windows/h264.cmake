# Build an overlay of the pinned LibVNCServer source. The dependency checkout
# stays pristine, and rebuilding from the packaged sources applies the same hooks.
set(rfbserver_original "${SVNC_DEPENDENCIES}/libvncserver/src/libvncserver/rfbserver.c")
set_property(DIRECTORY APPEND PROPERTY CMAKE_CONFIGURE_DEPENDS "${rfbserver_original}")
file(READ "${rfbserver_original}" rfbserver)
function(svnc_replace old new)
  string(FIND "${rfbserver}" "${old}" offset)
  if(offset EQUAL -1)
    message(FATAL_ERROR "Pinned LibVNCServer H.264 hook no longer matches: ${old}")
  endif()
  string(REPLACE "${old}" "${new}" updated "${rfbserver}")
  set(rfbserver "${updated}" PARENT_SCOPE)
endfunction()
svnc_replace("#include <rfb/rfb.h>" "#include <rfb/rfb.h>\n#include \"h264_bridge.h\"")
svnc_replace("        cl->preferredEncoding=-1;" "        svncH264Reset(cl);\n        if (lastPreferredEncoding == 50) lastPreferredEncoding = -1;\n        cl->preferredEncoding=-1;")
svnc_replace("            enc = Swap32IfLE(enc);\n\n            switch (enc)"
             "            enc = Swap32IfLE(enc);\n            svncH264Offered(cl, enc);\n\n            switch (enc)")
svnc_replace("            case rfbEncodingCopyRect:" "            case 50: /* noVNC H.264, negotiated explicitly */\n                if (cl->preferredEncoding == -1 && svncH264Select(cl))\n                    cl->preferredEncoding = 50;\n                break;\n            case rfbEncodingCopyRect:")
# After ordinary region accounting, encode one complete image so inter-frame
# references always use the same geometry. Cursor-only updates remain metadata.
svnc_replace("    rfbStatRecordMessageSent(cl, rfbFramebufferUpdate, 0, 0);"
"    if (cl->preferredEncoding == 50 && (!sraRgnEmpty(updateRegion) || !sraRgnEmpty(updateCopyRegion))) {\n        sraRgnDestroy(updateRegion);\n        updateRegion = sraRgnCreateRect(0, 0, cl->screen->width, cl->screen->height);\n        sraRgnMakeEmpty(updateCopyRegion);\n    }\n    rfbStatRecordMessageSent(cl, rfbFramebufferUpdate, 0, 0);")
svnc_replace("        case rfbEncodingRaw:\n            if (!rfbSendRectEncodingRaw" "        case 50:\n            if (!svncH264Send(cl)) goto updateFailed;\n            break;\n        case rfbEncodingRaw:\n            if (!rfbSendRectEncodingRaw")
file(WRITE "${CMAKE_CURRENT_BINARY_DIR}/rfbserver-h264.c" "${rfbserver}")
get_target_property(vncserver_sources vncserver SOURCES)
list(FILTER vncserver_sources EXCLUDE REGEX "/rfbserver\\.c$")
set_property(TARGET vncserver PROPERTY SOURCES "${vncserver_sources};${CMAKE_CURRENT_BINARY_DIR}/rfbserver-h264.c")
target_include_directories(vncserver PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}" "${SVNC_DEPENDENCIES}/libvncserver/src/libvncserver")
