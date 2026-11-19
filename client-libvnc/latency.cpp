// GPL-2.0-or-later. Input-to-decoded-image benchmark using LibVNCClient.
#include <rfb/rfbclient.h>
#include <rfb/keysym.h>
#include <chrono>
#include <algorithm>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <string>
#include <vector>
using Clock=std::chrono::steady_clock;
static int mx,my,frames;static bool pending,oldWhite,seen;static Clock::time_point sent;static double firstMs,completeMs;
static double elapsed(Clock::time_point t){return std::chrono::duration<double,std::milli>(Clock::now()-t).count();}
static bool white(rfbClient*c){auto*p=c->frameBuffer+((size_t)(my+20)*c->width+mx+20)*4;return (int)p[0]+p[1]+p[2]>384;}
static rfbBool allocate(rfbClient*c){if(c->width<1||c->height<1||(size_t)c->width*c->height>64000000)return FALSE;free(c->frameBuffer);c->frameBuffer=(uint8_t*)calloc((size_t)c->width*c->height,4);return c->frameBuffer!=nullptr;}
static char* password(rfbClient*){const char*home=getenv("HOME");if(!home)return nullptr;std::ifstream f(std::string(home)+"/.ssh/sshvnc-g15/vnc-password.txt");std::string s;std::getline(f,s);if(!s.empty()&&s.back()=='\r')s.pop_back();return s.empty()?nullptr:strdup(s.c_str());}
static void rectangle(rfbClient*c,int x,int y,int w,int h){if(pending&&!seen&&mx+20>=x&&mx+20<x+w&&my+20>=y&&my+20<y+h&&white(c)!=oldWhite){firstMs=elapsed(sent);seen=true;}}
static void finished(rfbClient*c){frames++;if(pending&&seen){completeMs=elapsed(sent);pending=false;}}
static bool pump(rfbClient*c,int milliseconds){auto t=Clock::now();while(elapsed(t)<milliseconds){int n=WaitForMessage(c,1000);if(n<0||(n&&!HandleRFBServerMessage(c)))return false;}return true;}
static double percentile(std::vector<double> v,double p){std::sort(v.begin(),v.end());return v[std::min(v.size()-1,(size_t)(p*(v.size()-1)))];}
int main(int argc,char**argv){
 if(argc!=7){fprintf(stderr,"usage: latency port marker-x marker-y quality region-only samples\n");return 2;}
 int port=atoi(argv[1]);mx=atoi(argv[2]);my=atoi(argv[3]);int quality=atoi(argv[4]),roi=atoi(argv[5]),samples=atoi(argv[6]);
 rfbClient*c=rfbGetClient(8,3,4);c->serverHost=strdup("127.0.0.1");c->serverPort=port;c->GetPassword=password;c->MallocFrameBuffer=allocate;c->GotFrameBufferUpdate=rectangle;c->FinishedFrameBufferUpdate=finished;
 c->appData.encodingsString="tight";c->appData.compressLevel=1;c->appData.qualityLevel=quality;c->appData.enableJPEG=TRUE;c->appData.useRemoteCursor=FALSE;c->appData.shareDesktop=TRUE;c->connectTimeout=10;c->readTimeout=10;
 int fakeargc=1;char name[]="latency";char*fakeargv[]={name,nullptr};if(!rfbInitClient(c,&fakeargc,fakeargv))return 3;
 if(mx<0||my<0||mx+20>=c->width||my+20>=c->height)return 4;
 if(roi){c->updateRect.x=mx-32;c->updateRect.y=my-32;c->updateRect.w=64;c->updateRect.h=64;}
 if(!pump(c,1200)||!frames)return 5;
 SendPointerEvent(c,mx,my,0);SendPointerEvent(c,mx,my,1);SendPointerEvent(c,mx,my,0);if(!pump(c,500))return 5;
 for(int mode=0;mode<2;mode++){
  std::vector<double> first,complete;int timeouts=0;
  for(int i=0;i<samples;i++){
   if(!pump(c,100))return 6;
   oldWhite=white(c);seen=false;pending=true;sent=Clock::now();
   if(mode==0){SendKeyEvent(c,XK_F8,TRUE);SendKeyEvent(c,XK_F8,FALSE);}else{SendPointerEvent(c,mx,my,0);SendPointerEvent(c,mx,my,1);SendPointerEvent(c,mx,my,0);}
   while(pending&&elapsed(sent)<3000){if(!pump(c,1))return 7;}
   if(pending){timeouts++;pending=false;}else{first.push_back(firstMs);complete.push_back(completeMs);}
  }
  if(first.empty()){fprintf(stderr,"No marker changes for %s\n",mode==0?"key":"mouse");return 8;}
  printf("{\"kind\":\"%s\",\"quality\":%d,\"region_only\":%d,\"n\":%zu,\"timeouts\":%d,\"pixel_p50_ms\":%.1f,\"pixel_p95_ms\":%.1f,\"frame_p50_ms\":%.1f,\"frame_p95_ms\":%.1f}\n",mode==0?"key":"mouse",quality,roi,first.size(),timeouts,percentile(first,.5),percentile(first,.95),percentile(complete,.5),percentile(complete,.95));fflush(stdout);
 }
 free(c->frameBuffer);c->frameBuffer=nullptr;rfbClientCleanup(c);return 0;
}
