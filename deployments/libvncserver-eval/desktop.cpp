// GPL-2.0-or-later. Experimental Windows desktop adapter for LibVNCServer.
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <winsock2.h>
#include <windows.h>
#include <d3d11.h>
#include <dxgi1_2.h>
#include <wrl/client.h>
#include <rfb/rfb.h>
#include <rfb/keysym.h>
#include <algorithm>
#include <cstdio>
#include <fstream>
#include <set>
#include <string>
#include <vector>
using Microsoft::WRL::ComPtr;
static std::set<WORD> keys;
static int buttons = 0;
static int originX, originY;
static void keyInput(WORD key, bool down, bool unicode=false) {
    INPUT i{}; i.type=INPUT_KEYBOARD;
    i.ki.wVk=unicode?0:key; i.ki.wScan=unicode?key:0;
    i.ki.dwFlags=(down?0:KEYEVENTF_KEYUP)|(unicode?KEYEVENTF_UNICODE:0);
    if (!unicode && (key==VK_RCONTROL || key==VK_RMENU || (key>=VK_PRIOR && key<=VK_DOWN) || key==VK_INSERT || key==VK_DELETE || key==VK_LWIN || key==VK_RWIN)) i.ki.dwFlags|=KEYEVENTF_EXTENDEDKEY;
    SendInput(1,&i,sizeof(i));
}
static void keyboard(rfbBool down, rfbKeySym sym, rfbClientPtr) {
    WORD vk=0;
    if(sym>=XK_F1 && sym<=XK_F24) vk=VK_F1+(sym-XK_F1);
    else switch(sym) {
    case XK_BackSpace:vk=VK_BACK;break; case XK_Tab:case XK_ISO_Left_Tab:vk=VK_TAB;break;
    case XK_Return:case XK_KP_Enter:vk=VK_RETURN;break; case XK_Escape:vk=VK_ESCAPE;break;
    case XK_Insert:vk=VK_INSERT;break; case XK_Delete:vk=VK_DELETE;break;
    case XK_Home:vk=VK_HOME;break; case XK_End:vk=VK_END;break;
    case XK_Page_Up:vk=VK_PRIOR;break; case XK_Page_Down:vk=VK_NEXT;break;
    case XK_Left:vk=VK_LEFT;break; case XK_Up:vk=VK_UP;break; case XK_Right:vk=VK_RIGHT;break; case XK_Down:vk=VK_DOWN;break;
    case XK_Shift_L:vk=VK_LSHIFT;break; case XK_Shift_R:vk=VK_RSHIFT;break;
    case XK_Control_L:vk=VK_LCONTROL;break; case XK_Control_R:vk=VK_RCONTROL;break;
    case XK_Alt_L:vk=VK_LMENU;break; case XK_Alt_R:vk=VK_RMENU;break;
    case XK_Super_L:case XK_Meta_L:vk=VK_LWIN;break; case XK_Super_R:case XK_Meta_R:vk=VK_RWIN;break;
    case XK_Caps_Lock:vk=VK_CAPITAL;break; case XK_Num_Lock:vk=VK_NUMLOCK;break;
    case XK_Print:vk=VK_SNAPSHOT;break; case XK_Pause:vk=VK_PAUSE;break;
    default: if(sym>=32 && sym<127) { SHORT k=VkKeyScanW((WCHAR)sym); if(k!=-1) vk=LOBYTE(k); }
    }
    if(vk) { keyInput(vk,down!=0); if(down) keys.insert(vk); else keys.erase(vk); }
    else if(down) {
        unsigned cp=(sym&0xff000000)==0x01000000?sym&0xffffff:sym;
        if(cp>=32 && cp<=0x10ffff && !(cp>=0xd800 && cp<=0xdfff)) {
            if(cp<=0xffff) { keyInput((WORD)cp,true,true); keyInput((WORD)cp,false,true); }
            else { cp-=0x10000; for(WORD c : {(WORD)(0xd800+(cp>>10)),(WORD)(0xdc00+(cp&1023))}) {keyInput(c,true,true);keyInput(c,false,true);} }
        }
    }
}
static void pointer(int mask,int x,int y,rfbClientPtr cl) {
    INPUT i{}; i.type=INPUT_MOUSE;
    int vx=GetSystemMetrics(SM_XVIRTUALSCREEN),vy=GetSystemMetrics(SM_YVIRTUALSCREEN);
    int vw=GetSystemMetrics(SM_CXVIRTUALSCREEN),vh=GetSystemMetrics(SM_CYVIRTUALSCREEN);
    i.mi.dx=(LONG)((long long)(x+originX-vx)*65535/std::max(1,vw-1));
    i.mi.dy=(LONG)((long long)(y+originY-vy)*65535/std::max(1,vh-1));
    i.mi.dwFlags=MOUSEEVENTF_MOVE|MOUSEEVENTF_ABSOLUTE|MOUSEEVENTF_VIRTUALDESK;
    SendInput(1,&i,sizeof(i));
    const DWORD downs[]={MOUSEEVENTF_LEFTDOWN,MOUSEEVENTF_MIDDLEDOWN,MOUSEEVENTF_RIGHTDOWN};
    const DWORD ups[]={MOUSEEVENTF_LEFTUP,MOUSEEVENTF_MIDDLEUP,MOUSEEVENTF_RIGHTUP};
    for(int b=0;b<3;b++) if((mask^buttons)&(1<<b)) {i={};i.type=INPUT_MOUSE;i.mi.dwFlags=(mask&(1<<b))?downs[b]:ups[b];SendInput(1,&i,sizeof(i));}
    if((mask&24) && !(buttons&24)) {i={};i.type=INPUT_MOUSE;i.mi.dwFlags=MOUSEEVENTF_WHEEL;i.mi.mouseData=(mask&8)?WHEEL_DELTA:(DWORD)-WHEEL_DELTA;SendInput(1,&i,sizeof(i));}
    buttons=mask; rfbDefaultPtrAddEvent(mask,x,y,cl);
}
static void releaseKeys(rfbClientPtr) {
    for(WORD k:keys) keyInput(k,false); keys.clear();
    INPUT i{};i.type=INPUT_MOUSE;i.mi.dwFlags=MOUSEEVENTF_LEFTUP|MOUSEEVENTF_MIDDLEUP|MOUSEEVENTF_RIGHTUP;SendInput(1,&i,sizeof(i));buttons=0;
}
static enum rfbNewClientAction connected(rfbClientPtr cl) { cl->clientGoneHook=releaseKeys; return RFB_CLIENT_ACCEPT; }
struct Capture {
    ComPtr<ID3D11Device> device; ComPtr<ID3D11DeviceContext> context;
    ComPtr<IDXGIOutputDuplication> duplication; ComPtr<ID3D11Texture2D> staging;
    int width=0,height=0;
    bool init() {
        duplication.Reset();staging.Reset();context.Reset();device.Reset();
        ComPtr<IDXGIFactory1> factory;
        if(FAILED(CreateDXGIFactory1(IID_PPV_ARGS(&factory)))) return false;
        HMONITOR primary=MonitorFromPoint(POINT{0,0},MONITOR_DEFAULTTOPRIMARY);
        for(UINT a=0;;a++) {
            ComPtr<IDXGIAdapter1> adapter; if(factory->EnumAdapters1(a,&adapter)==DXGI_ERROR_NOT_FOUND)break;
            for(UINT o=0;;o++) {
                ComPtr<IDXGIOutput> output; if(adapter->EnumOutputs(o,&output)==DXGI_ERROR_NOT_FOUND)break;
                DXGI_OUTPUT_DESC desc{};output->GetDesc(&desc);
                if(desc.Monitor!=primary || !desc.AttachedToDesktop)continue;
                if(desc.Rotation!=DXGI_MODE_ROTATION_IDENTITY) {fprintf(stderr,"Rotated displays are not supported by this trial.\n");return false;}
                D3D_FEATURE_LEVEL level;
                HRESULT hr=D3D11CreateDevice(adapter.Get(),D3D_DRIVER_TYPE_UNKNOWN,nullptr,0,nullptr,0,D3D11_SDK_VERSION,&device,&level,&context);
                if(FAILED(hr))return false;
                ComPtr<IDXGIOutput1> output1; if(FAILED(output.As(&output1)))return false;
                hr=output1->DuplicateOutput(device.Get(),&duplication);
                if(FAILED(hr)) {fprintf(stderr,"DuplicateOutput failed: 0x%08lx\n",hr);return false;}
                DXGI_OUTDUPL_DESC dd{};duplication->GetDesc(&dd);
                width=(int)dd.ModeDesc.Width;height=(int)dd.ModeDesc.Height;
                originX=desc.DesktopCoordinates.left;originY=desc.DesktopCoordinates.top;
                D3D11_TEXTURE2D_DESC td{};td.Width=width;td.Height=height;td.MipLevels=1;td.ArraySize=1;
                td.Format=DXGI_FORMAT_B8G8R8A8_UNORM;td.SampleDesc.Count=1;td.Usage=D3D11_USAGE_STAGING;td.CPUAccessFlags=D3D11_CPU_ACCESS_READ;
                return SUCCEEDED(device->CreateTexture2D(&td,nullptr,&staging));
            }
        }
        return false;
    }
    int update(rfbScreenInfoPtr screen) {
        DXGI_OUTDUPL_FRAME_INFO info{};ComPtr<IDXGIResource> resource;
        HRESULT hr=duplication->AcquireNextFrame(0,&info,&resource);
        if(hr==DXGI_ERROR_WAIT_TIMEOUT)return 0;
        if(FAILED(hr))return -1;
        if(info.LastPresentTime.QuadPart==0) {duplication->ReleaseFrame();return 0;}
        ComPtr<ID3D11Texture2D> texture;hr=resource.As(&texture);
        if(FAILED(hr)){duplication->ReleaseFrame();return -1;}
        context->CopyResource(staging.Get(),texture.Get());
        D3D11_MAPPED_SUBRESOURCE map{};hr=context->Map(staging.Get(),0,D3D11_MAP_READ,0,&map);
        if(FAILED(hr)){duplication->ReleaseFrame();return -1;}
        // Coalesce changed 64x64 tiles into horizontal runs. Never send unchanged pixels.
        for(int y=0;y<height;y+=64) {
            int y2=std::min(y+64,height),run=-1;
            for(int x=0;x<width;x+=64) {
                int x2=std::min(x+64,width);bool changed=false;
                for(int row=y;row<y2 && !changed;row++) changed=memcmp(screen->frameBuffer+((size_t)row*width+x)*4,(char*)map.pData+(size_t)row*map.RowPitch+x*4,(x2-x)*4)!=0;
                if(changed) {
                    for(int row=y;row<y2;row++) memcpy(screen->frameBuffer+((size_t)row*width+x)*4,(char*)map.pData+(size_t)row*map.RowPitch+x*4,(x2-x)*4);
                    if(run<0)run=x;
                } else if(run>=0) {rfbMarkRectAsModified(screen,run,y,x,y2);run=-1;}
            }
            if(run>=0)rfbMarkRectAsModified(screen,run,y,width,y2);
        }
        context->Unmap(staging.Get(),0);duplication->ReleaseFrame();return 1;
    }
};
int main(int argc,char** argv) {
    SetProcessDPIAware();setvbuf(stdout,nullptr,_IONBF,0);setvbuf(stderr,nullptr,_IONBF,0);
    std::ifstream file("vnc-password.txt");std::string password;std::getline(file,password);
    if(!password.empty() && password.back()=='\r')password.pop_back();
    if(password.empty() || password.size()>8) {fprintf(stderr,"Expected a private vnc-password.txt containing 1-8 bytes.\n");return 1;}
    Capture capture;if(!capture.init()) {fprintf(stderr,"Cannot initialize DXGI in the current desktop session.\n");return 2;}
    rfbScreenInfoPtr s=rfbGetScreen(&argc,argv,capture.width,capture.height,8,3,4);if(!s)return 3;
    std::vector<char> pixels((size_t)capture.width*capture.height*4);s->frameBuffer=pixels.data();
    s->serverFormat.redShift=16;s->serverFormat.greenShift=8;s->serverFormat.blueShift=0;
    s->serverFormat.redMax=s->serverFormat.greenMax=s->serverFormat.blueMax=255;
    s->desktopName="g15 - LibVNCServer 0.9.15 / DXGI / Tight JPEG";
    s->listenInterface=htonl(INADDR_LOOPBACK);s->port=5902;
#ifdef LIBVNCSERVER_IPv6
    s->ipv6port=-1;
#endif
    s->httpPort=-1;s->httpDir=nullptr;s->alwaysShared=TRUE;
    char* passwords[]={const_cast<char*>(password.c_str()),nullptr};
    s->authPasswdData=passwords;s->passwordCheck=rfbCheckPasswordByList;
    s->kbdAddEvent=keyboard;s->ptrAddEvent=pointer;s->kbdReleaseAllKeys=releaseKeys;s->newClientHook=connected;
    s->deferUpdateTime=5;s->deferPtrUpdateTime=0;s->handleEventsEagerly=TRUE;s->maxClientWait=10000;
    rfbInitServer(s);if(s->listenSock==RFB_INVALID_SOCKET)return 4;
    printf("READY LibVNCServer=%s capture=DXGI primary=%dx%d origin=%d,%d listen=127.0.0.1:5902 max_fps=60 JPEG=SIMD\n",LIBVNCSERVER_PACKAGE_VERSION,capture.width,capture.height,originX,originY);
    ULONGLONG next=0,report=GetTickCount64();int frames=0;double captureMs=0,eventMs=0;int loops=0;
    while(rfbIsActive(s)) {
        ULONGLONG now=GetTickCount64();
        if(s->clientHead && now>=next) {
            next=now+16;LARGE_INTEGER f,a,b;QueryPerformanceFrequency(&f);QueryPerformanceCounter(&a);
            int result=capture.update(s);QueryPerformanceCounter(&b);captureMs+=1000.0*(b.QuadPart-a.QuadPart)/f.QuadPart;
            if(result<0) {
                fprintf(stderr,"DXGI access lost, reinitializing.\n");Sleep(100);
                int w=capture.width,h=capture.height;
                if(!capture.init() || capture.width!=w || capture.height!=h) {fprintf(stderr,"Restart trial after desktop/display change.\n");return 5;}
            }
            if(result>0)frames++;
        }
        LARGE_INTEGER f,a,b;QueryPerformanceFrequency(&f);QueryPerformanceCounter(&a);
        rfbProcessEvents(s,1000);QueryPerformanceCounter(&b);eventMs+=1000.0*(b.QuadPart-a.QuadPart)/f.QuadPart;loops++;
        if(now-report>=5000) {printf("sample_ms=%llu captured_frames=%d capture_diff_ms=%.1f event_encode_io_ms=%.1f loops=%d clients=%d\n",now-report,frames,captureMs,eventMs,loops,s->clientHead?1:0);report=now;frames=0;captureMs=eventMs=0;loops=0;}
    }
    releaseKeys(nullptr);rfbScreenCleanup(s);return 0;
}
