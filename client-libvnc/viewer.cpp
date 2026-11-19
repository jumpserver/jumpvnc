// GPL-2.0-or-later. Native SDL2 viewer for the g15 LibVNCServer evaluation.
#include <SDL.h>
#include <rfb/rfbclient.h>
#include <rfb/keysym.h>
#include <algorithm>
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <deque>
#include <fstream>
#include <mutex>
#include <set>
#include <string>
#include <thread>
#include <vector>
#include <sys/socket.h>
using Clock=std::chrono::steady_clock;
struct Event {bool key;int x,y,mask;uint32_t sym;bool down;};
static std::atomic<bool> running{true},ready{false},failed{false};
static std::atomic<int> desiredQuality{6},port{15903},videoSocket{-1},inputSocket{-1};
static std::atomic<unsigned> updates{0};
static std::atomic<double> maxInputQueueMs{0};
static std::mutex frameMutex,inputMutex,errorMutex;
static std::condition_variable inputCondition;
static std::deque<std::pair<Event,Clock::time_point>> events;
static std::vector<uint8_t> frame;static int fw=0,fh=0;static bool dirty=false;
static std::string failure;
static void fail(const char*s){std::lock_guard<std::mutex>lock(errorMutex);failure=s;failed=true;running=false;inputCondition.notify_all();}
static char* password(rfbClient*){
 const char*home=getenv("HOME");if(!home)return nullptr;
 std::ifstream f(std::string(home)+"/.ssh/sshvnc-g15/vnc-password.txt");std::string s;std::getline(f,s);if(!s.empty()&&s.back()=='\r')s.pop_back();return s.empty()?nullptr:strdup(s.c_str());
}
static rfbClient* newClient(){
 auto*c=rfbGetClient(8,3,4);c->serverHost=strdup("127.0.0.1");c->serverPort=port;c->GetPassword=password;
 c->appData.shareDesktop=TRUE;c->appData.encodingsString="tight copyrect";c->appData.qualityLevel=desiredQuality;c->appData.compressLevel=1;c->appData.enableJPEG=TRUE;c->appData.useRemoteCursor=TRUE;
 c->format.redShift=16;c->format.greenShift=8;c->format.blueShift=0;c->connectTimeout=8;c->readTimeout=5;return c;
}
static rfbBool allocate(rfbClient*c){
 if(c->width<1||c->height<1||(size_t)c->width*c->height>64000000)return FALSE;
 free(c->frameBuffer);c->frameBuffer=(uint8_t*)calloc((size_t)c->width*c->height,4);return c->frameBuffer!=nullptr;
}
static void rectangle(rfbClient*,int,int,int,int){}
static void completed(rfbClient*c){
 std::lock_guard<std::mutex>lock(frameMutex);fw=c->width;fh=c->height;
 frame.assign(c->frameBuffer,c->frameBuffer+(size_t)fw*fh*4);dirty=true;updates++;
}
static void videoThread(){
 rfbClient*c=newClient();c->MallocFrameBuffer=allocate;c->GotFrameBufferUpdate=rectangle;c->FinishedFrameBufferUpdate=completed;c->canHandleNewFBSize=TRUE;
 int argc=1;char name[]="LibVNCClient";char*argv[]={name,nullptr};
 if(!rfbInitClient(c,&argc,argv)){fail("Cannot connect to g15. Check the SSH tunnel on local port 15903.");return;}
 videoSocket=c->sock;
 if(!c->desktopName||!strstr(c->desktopName,"g15 - LibVNCServer")){fail("Port 15903 is connected to an unexpected desktop backend.");}
 int quality=desiredQuality;
 while(running){
  if(quality!=desiredQuality){quality=desiredQuality;c->appData.qualityLevel=quality;if(!SetFormatAndEncodings(c))break;}
  int n=WaitForMessage(c,5000);if(n<0||(n&&!HandleRFBServerMessage(c)))break;
 }
 if(running)fail("Desktop connection closed. Reopen the viewer to reconnect.");
 videoSocket=-1;free(c->frameBuffer);c->frameBuffer=nullptr;rfbClientCleanup(c);
}
static void inputThread(){
 rfbClient*c=newClient();
 // Authenticate an independent input-only RFB connection. No framebuffer
 // request is issued, so image reads and JPEG decoding never hold input up.
 if(!ConnectToRFBServer(c,c->serverHost,c->serverPort)||!InitialiseRFBConnection(c)){rfbClientCleanup(c);fail("Cannot authenticate the input connection.");return;}
 inputSocket=c->sock;ready=true;
 while(running){
  std::deque<std::pair<Event,Clock::time_point>> batch;
  {std::unique_lock<std::mutex>lock(inputMutex);inputCondition.wait_for(lock,std::chrono::milliseconds(100),[]{return !events.empty()||!running;});batch.swap(events);}
  for(auto&item:batch){
   const auto&e=item.first;double ms=std::chrono::duration<double,std::milli>(Clock::now()-item.second).count();double old=maxInputQueueMs;while(old<ms&&!maxInputQueueMs.compare_exchange_weak(old,ms)){}
   bool ok=e.key?SendKeyEvent(c,e.sym,e.down):SendPointerEvent(c,e.x,e.y,e.mask);
   if(!ok){if(running)fail("Input connection closed. Reopen the viewer to reconnect.");break;}
  }
 }
 inputSocket=-1;rfbClientCleanup(c);
}
static void enqueue(Event e){
 std::lock_guard<std::mutex>lock(inputMutex);
 // Only consecutive movements with identical buttons may be coalesced.
 // Button transitions, wheel events and keyboard events remain ordered.
 if(!e.key&&!events.empty()&&!events.back().first.key&&events.back().first.mask==e.mask&&!(e.mask&24))events.back()={e,Clock::now()};
 else events.push_back({e,Clock::now()});
 inputCondition.notify_one();
}
static uint32_t keysym(SDL_Keycode k){
 if(k>=32&&k<127)return (uint32_t)k;
 if(k>=SDLK_F1&&k<=SDLK_F12)return XK_F1+(k-SDLK_F1);
 switch(k){
 case SDLK_BACKSPACE:return XK_BackSpace;case SDLK_TAB:return XK_Tab;case SDLK_RETURN:return XK_Return;case SDLK_ESCAPE:return XK_Escape;case SDLK_DELETE:return XK_Delete;
 case SDLK_HOME:return XK_Home;case SDLK_END:return XK_End;case SDLK_PAGEUP:return XK_Page_Up;case SDLK_PAGEDOWN:return XK_Page_Down;case SDLK_INSERT:return XK_Insert;
 case SDLK_LEFT:return XK_Left;case SDLK_RIGHT:return XK_Right;case SDLK_UP:return XK_Up;case SDLK_DOWN:return XK_Down;
 case SDLK_LSHIFT:return XK_Shift_L;case SDLK_RSHIFT:return XK_Shift_R;case SDLK_LCTRL:return XK_Control_L;case SDLK_RCTRL:return XK_Control_R;
 case SDLK_LALT:return XK_Alt_L;case SDLK_RALT:return XK_Alt_R;case SDLK_LGUI:return XK_Super_L;case SDLK_RGUI:return XK_Super_R;
 case SDLK_CAPSLOCK:return XK_Caps_Lock;case SDLK_NUMLOCKCLEAR:return XK_Num_Lock;default:return 0;}
}
static SDL_Rect fit(int w,int h,int rw,int rh){double scale=std::min((double)w/rw,(double)h/rh);int x=(int)(rw*scale),y=(int)(rh*scale);return {(w-x)/2,(h-y)/2,x,y};}
static uint32_t nextUnicode(const unsigned char*&p){uint32_t c=*p++;if(c<128)return c;int n=(c&0xe0)==0xc0?1:(c&0xf0)==0xe0?2:(c&0xf8)==0xf0?3:0;c&=(1<<(6-n))-1;while(n--&&*p){if((*p&0xc0)!=0x80)return 0;c=(c<<6)|(*p++&63);}return c;}
int main(int argc,char**argv){
 bool smoke=false;for(int i=1;i<argc;i++){if(!strcmp(argv[i],"--smoke-test"))smoke=true;else if(!strcmp(argv[i],"--quality")&&i+1<argc)desiredQuality=std::clamp(atoi(argv[++i]),0,9);}
 if(SDL_Init(SDL_INIT_VIDEO|SDL_INIT_EVENTS)!=0){fprintf(stderr,"SDL: %s\n",SDL_GetError());return 1;}
 SDL_SetHint(SDL_HINT_RENDER_SCALE_QUALITY,"linear");
 auto*window=SDL_CreateWindow("LibVNCClient - connecting to g15",SDL_WINDOWPOS_CENTERED,SDL_WINDOWPOS_CENTERED,1280,760,SDL_WINDOW_RESIZABLE|SDL_WINDOW_ALLOW_HIGHDPI);
 auto*renderer=window?SDL_CreateRenderer(window,-1,SDL_RENDERER_ACCELERATED):nullptr;
 if(!renderer){fprintf(stderr,"SDL renderer: %s\n",SDL_GetError());SDL_Quit();return 1;}
 SDL_SetRenderDrawColor(renderer,25,25,28,255);SDL_StartTextInput();
 std::thread video(videoThread),input(inputThread);SDL_Texture*texture=nullptr;int tw=0,th=0,mx=0,my=0,mask=0;std::set<uint32_t>held;std::set<SDL_Keycode>localHotkeys;unsigned totalFrames=0;
 auto report=Clock::now(),start=report;bool repaint=true;
 while(running){
  SDL_Event e;while(SDL_PollEvent(&e)){
   if(e.type==SDL_QUIT){running=false;break;}
   if(e.type==SDL_WINDOWEVENT){repaint=true;if(e.window.event==SDL_WINDOWEVENT_FOCUS_LOST){for(auto k:held)enqueue({true,0,0,0,k,false});held.clear();if(mask)enqueue({false,mx,my,0,0,false});mask=0;}}
   if(e.type==SDL_KEYDOWN||e.type==SDL_KEYUP){
    bool down=e.type==SDL_KEYDOWN;auto k=e.key.keysym.sym;
    if(down&&(e.key.keysym.mod&KMOD_CTRL)&&(e.key.keysym.mod&KMOD_ALT)&&k>=SDLK_1&&k<=SDLK_3){desiredQuality=(k==SDLK_1?3:k==SDLK_2?6:9);localHotkeys.insert(k);continue;}
    if(!down&&localHotkeys.erase(k))continue;
    uint32_t sym=keysym(k);if(sym){enqueue({true,0,0,0,sym,down});if(down)held.insert(sym);else held.erase(sym);}
   }
   if(e.type==SDL_TEXTINPUT){auto*p=(const unsigned char*)e.text.text;while(*p){uint32_t cp=nextUnicode(p);if(cp>127&&cp<=0x10ffff){enqueue({true,0,0,0,0x01000000|cp,true});enqueue({true,0,0,0,0x01000000|cp,false});}}}
   if(e.type==SDL_MOUSEMOTION||e.type==SDL_MOUSEBUTTONDOWN||e.type==SDL_MOUSEBUTTONUP){
    if(!tw||!th)continue;int w,h;SDL_GetWindowSize(window,&w,&h);auto r=fit(w,h,tw,th);int x=e.type==SDL_MOUSEMOTION?e.motion.x:e.button.x,y=e.type==SDL_MOUSEMOTION?e.motion.y:e.button.y;
    if(x<r.x||x>=r.x+r.w||y<r.y||y>=r.y+r.h){if(e.type==SDL_MOUSEBUTTONUP&&mask){mask=0;enqueue({false,mx,my,mask,0,false});}continue;}
    mx=std::clamp((x-r.x)*tw/r.w,0,tw-1);my=std::clamp((y-r.y)*th/r.h,0,th-1);
    if(e.type!=SDL_MOUSEMOTION){int bit=e.button.button==SDL_BUTTON_LEFT?1:e.button.button==SDL_BUTTON_MIDDLE?2:e.button.button==SDL_BUTTON_RIGHT?4:0;if(e.type==SDL_MOUSEBUTTONDOWN)mask|=bit;else mask&=~bit;}
    enqueue({false,mx,my,mask,0,false});
   }
   if(e.type==SDL_MOUSEWHEEL&&e.wheel.y){int direction=e.wheel.direction==SDL_MOUSEWHEEL_FLIPPED?-1:1;int bit=e.wheel.y*direction>0?8:16;for(int n=0;n<std::min(8,abs(e.wheel.y));n++){enqueue({false,mx,my,mask|bit,0,false});enqueue({false,mx,my,mask,0,false});}}
  }
  {
   std::lock_guard<std::mutex>lock(frameMutex);
   if(dirty){if(fw!=tw||fh!=th){if(texture)SDL_DestroyTexture(texture);tw=fw;th=fh;texture=SDL_CreateTexture(renderer,SDL_PIXELFORMAT_ARGB8888,SDL_TEXTUREACCESS_STREAMING,tw,th);}
    if(!texture||SDL_UpdateTexture(texture,nullptr,frame.data(),fw*4)<0){fail("Cannot upload the desktop image to SDL.");break;}dirty=false;repaint=true;}
  }
  if(repaint){SDL_RenderClear(renderer);if(texture){int w,h;SDL_GetRendererOutputSize(renderer,&w,&h);auto r=fit(w,h,tw,th);SDL_RenderCopy(renderer,texture,nullptr,&r);}SDL_RenderPresent(renderer);repaint=false;}
  auto now=Clock::now();if(now-report>=std::chrono::seconds(1)){
   double seconds=std::chrono::duration<double>(now-report).count();unsigned n=updates.exchange(0);totalFrames+=n;double queue=maxInputQueueMs.exchange(0);char title[256];
   snprintf(title,sizeof(title),"LibVNCClient | g15 | Q%d | %.1f updates/s | input queue max %.1f ms | Ctrl+Alt+1/2/3 quality",(int)desiredQuality,n/seconds,queue);SDL_SetWindowTitle(window,title);report=now;
  }
  if(smoke&&now-start>std::chrono::seconds(8))running=false;
  SDL_Delay(2);
 }
 inputCondition.notify_all();int vs=videoSocket.load();if(vs>=0)shutdown(vs,SHUT_RDWR);int is=inputSocket.load();if(is>=0)shutdown(is,SHUT_RDWR);
 video.join();input.join();
 if(failed){std::lock_guard<std::mutex>lock(errorMutex);SDL_ShowSimpleMessageBox(SDL_MESSAGEBOX_ERROR,"LibVNCClient",failure.c_str(),window);}
 if(texture)SDL_DestroyTexture(texture);SDL_DestroyRenderer(renderer);SDL_DestroyWindow(window);SDL_Quit();
 if(smoke){printf("smoke_test frames=%u input_authenticated=%d failed=%d\n",totalFrames,(int)ready,(int)failed);return failed||!totalFrames||!ready;}
 return failed?1:0;
}
