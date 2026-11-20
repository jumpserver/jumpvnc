#!/bin/sh
set -eu
umask 077
: "${DISPLAY:?}" "${XAUTHORITY:?}"
case "${SVNC_DESKTOP:-xfce}" in
    xfce)
        session=/usr/bin/xfce4-session
        export XDG_CURRENT_DESKTOP=XFCE XDG_SESSION_DESKTOP=xfce
        ;;
    plasma)
        session=/usr/bin/startplasma-x11
        export XDG_CURRENT_DESKTOP=KDE XDG_SESSION_DESKTOP=KDE
        export QT_QPA_PLATFORM=xcb
        ;;
    *)
        echo 'SVNC_DESKTOP must be xfce or plasma' >&2
        exit 1
        ;;
esac
test -x "$session"
: > "$XAUTHORITY"
/usr/bin/xauth -f "$XAUTHORITY" add "$DISPLAY" . "$(/usr/bin/mcookie)"
exec /usr/bin/xinit /usr/bin/dbus-run-session "$session" -- \
    /usr/bin/Xvfb "$DISPLAY" -screen 0 1600x900x24 -nolisten tcp -auth "$XAUTHORITY" -noreset
