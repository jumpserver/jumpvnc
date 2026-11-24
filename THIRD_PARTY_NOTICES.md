# Third-party notices

JumpVNC is distributed under the GNU General Public License version 3; see
[LICENSE](LICENSE). Files with an existing SPDX identifier or copyright notice
retain that notice. In particular, the desktop adapters marked
`GPL-2.0-or-later` may be used under GPLv3, and the noVNC adaptations marked
`MPL-2.0` retain the Mozilla Public License.

The project uses these third-party components:

| Component | License / notice |
| --- | --- |
| LibVNCServer / LibVNCClient | GPL-2.0-or-later; the upstream GPL text is retained in `desktop/COPYING` and `desktop/windows/COPYING`. |
| noVNC | MPL-2.0 and component-specific notices in the bundled `LICENSE.txt`; matching source is included in the Viewer package. |
| libjpeg-turbo | Its upstream `LICENSE.md` (IJG, BSD and zlib terms for the respective files). |
| zlib | zlib license. |
| MyGo | MIT. |
| ssh_config | MIT; parses SSH host aliases and jump-host configuration without executing OpenSSH. |
| Go dependencies | Their upstream license and copyright notices are included in the vendored corresponding sources shipped in each package. |
| Linux system libraries | X11 and optional FFmpeg components retain the licenses supplied by the target distribution. |
| Microsoft Visual C++ Redistributable | Microsoft software license terms; bundled unmodified as a Windows installation prerequisite. |

Release packages include matching JumpVNC source and build scripts under
`sources/`. Windows packages also include the pinned desktop dependency sources
and their license texts under `sources/` and `licenses/`. The license text for
noVNC is included in the Viewer application's resources on all platforms.
