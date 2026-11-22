'use strict';
/* Browser-free ES5 regression harness for the LG webOS PiP client. */
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');

const root=path.resolve(__dirname,'../..');
const client=fs.readFileSync(path.join(root,'www/webos-pip/camera.js'),'utf8');
const config=fs.readFileSync(path.join(root,'webos/config.js'),'utf8');
assert(!/tapo_c230|192\.168\.|rtsp:\/\//.test(client+config),'no device-bound config');
assert(!/attemptMSE|Go2RTCWebOSMSE\.connect|video\.play\(/.test(client),
       'PiP must never acquire an MSE/hardware video decoder');
assert(client.includes('/api/stream.mjpeg?src=') &&
       client.includes('/api/frame.jpeg?src='),
       'JPEG multipart stream and bounded snapshot fallback both retained');
const html=fs.readFileSync(path.join(root,'www/webos-pip/index.html'),'utf8');
assert.equal((html.match(/\{\{APP_VERSION\}\}/g)||[]).length,2,
  'menu and diagnostics must use one package-stamped application version');
assert(html.includes('id="menuTitle">КАМЕРА · go2rtc · v{{APP_VERSION}}'),
       'ellipsis menu exposes installed application build version');
assert(!/<video\b|src="mse-player.js"/.test(html),
       'PiP must not instantiate a hidden second video player');

function environment(url, launchParams, clock, sharedStorage, Bridge){
  const dom={}, events={}, xhr=[], saved=sharedStorage||{}, timers=[], intervals=[], created=[];
  let reloads=0, closed=0;
  function element(name){
    const item={id:name,tagName:name.toUpperCase(),hidden:false,children:[],
      style:{},attrs:{},parentNode:null,src:'',value:'',textContent:'',
      naturalWidth:640,naturalHeight:480,clientWidth:name==='pip'?480:0,
      clientHeight:name==='pip'?270:0,
      appendChild(child){child.parentNode=this;this.children.push(child);return child;},
      removeChild(child){this.children.splice(this.children.indexOf(child),1);child.parentNode=null;},
      removeAttribute(attr){delete this.attrs[attr];if(attr==='src')this.src='';},
      setAttribute(attr,value){this.attrs[attr]=String(value);},
      getAttribute(attr){return this.attrs[attr]||null;},
      click(){if(this.onclick)this.onclick();},
      get firstChild(){return this.children[0]||null;},
      focus(){},pause(){},play(){return Promise.resolve();},load(){}};
    created.push(item);return item;
  }
  for(const id of ['pip','tiles','frame','video','empty','status','statusLabel','sound',
       'menu','menuItems','menuButton','close','toast','setup','serverAddress','serverSave',
       'ptzButton','ptzPanel','ptzGrid','ptzHint','soundPanel','soundMonitor','soundListen',
       'soundInfo','audioInfo','cameraAudio','diagPanel','diagText','diagClose']){
    dom[id]=element(id);
  }
  dom.serverAddress.tagName='INPUT';
  dom.serverSave.tagName=dom.menuButton.tagName=dom.close.tagName=dom.diagClose.tagName='BUTTON';
  dom.ptzButton.tagName=dom.sound.tagName=dom.soundMonitor.tagName=
    dom.soundListen.tagName='BUTTON';
  for(const dir of [null,'up',null,'left','stop','right',null,'down',null]){
    const control=element(dir?'button':'span');
    if(dir)control.setAttribute('data-dir',dir);
    dom.ptzGrid.appendChild(control);
  }
  dom.tiles.appendChild(element('tile'));
  dom.tiles.children[0].appendChild(dom.frame);
  dom.tiles.children[0].appendChild(dom.video);
  const doc={hidden:false,getElementById(id){return dom[id];},
    createElement:element,addEventListener(k,fn){events[k]=fn;}};
  class XMLHttpRequest {
    open(method,target){this.method=method;this.url=target;this.headers={};}
    setRequestHeader(name,value){this.headers[name]=value;}
    send(data){this.data=data;xhr.push(this);}
    abort(){this.aborted=true;}
    respond(status,body){
      this.status=status;this.responseText=JSON.stringify(body);this.readyState=4;
      if(this.onreadystatechange)this.onreadystatechange();
    }
  }
  let n=0;
  const location={protocol:'file:',origin:'null',host:'',reload(){reloads++;}};
  const win={PIP_CONFIG:{url:url||'',refresh_ms:220,catalog_poll_ms:15000},
    location,close(){closed++;},webOSSystem:{activate(){}},
    PalmSystem:launchParams?{launchParams:JSON.stringify(launchParams)}:null,
    PalmServiceBridge:Bridge||null,
    addEventListener(type,fn){events['win:'+type]=fn;}};
  vm.runInNewContext(client,{
    window:win,document:doc,XMLHttpRequest,
    localStorage:{getItem(k){return saved[k]||null;},setItem(k,v){saved[k]=String(v);}},
    setTimeout(fn,ms){timers.push({fn,ms});return ++n;},clearTimeout(){},setInterval(fn,ms){intervals.push({fn,ms});return ++n;},
    clearInterval(){},Date:clock?{now(){return clock.now;}}:Date,Math,JSON,Number,String,Array,Object,RegExp,encodeURIComponent
  },{timeout:1000});
  return {dom,doc,events,xhr,saved,timers,intervals,created,win,reloads:()=>reloads,closed:()=>closed};
}

// 0.1.18: try webOS's own overlay-idle timer API. Never dispatch synthetic
// keyboard/pointer events or treat a failed/private API as protection.
const guardRequests=[];
function GuardBridge(){}
GuardBridge.prototype.call=function(uri,data){
  guardRequests.push({uri,data,bridge:this});
};
const guardClock={now:1000};
const protectedOverlay=environment('http://go2rtc.test:1984',null,guardClock,{},GuardBridge);
const initialGuardTick=protectedOverlay.timers.find(x=>x.ms===2500);
assert(initialGuardTick,'overlay guard attempts the system timer shortly after launch');
initialGuardTick.fn();
assert.equal(guardRequests.length,1,'guard uses a single bounded Luna request');
assert.equal(guardRequests[0].uri,
  'luna://com.webos.surfacemanager.screenSaver/restartUserActivityTimer');
assert.equal(guardRequests[0].data,'{}','no device identity or token sent to webOS');
guardRequests[0].bridge.onservicecallback('{"returnValue":true}');
protectedOverlay.events.keydown({keyCode:406,preventDefault(){}});
const guardDialog=protectedOverlay.dom.menuItems.children.find(
  x=>x.textContent==='Диагностика окна');
guardDialog.onclick();
assert(protectedOverlay.dom.diagText.textContent.includes('Таймер overlay: ответ получен (1)'),
  'diagnostic reports acknowledged timer restart, not a proven unlimited lifetime');
assert(protectedOverlay.dom.diagText.textContent.includes('Последнее JS-событие кадра'),
  'MJPEG diagnostics no longer claim browser onload is every decoded frame');
guardClock.now=48000;
const repeatGuard=protectedOverlay.intervals.find(x=>x.ms===45000);
assert(repeatGuard,'guard runs at most every 45 seconds');
repeatGuard.fn();
assert.equal(guardRequests.length,2,'visible overlay sends next bounded request');
guardRequests[1].bridge.onservicecallback('{"returnValue":true}');
protectedOverlay.doc.hidden=true;repeatGuard.fn();
assert.equal(guardRequests.length,2,'hidden overlay never restarts system timer');

const deniedRequests=[];
function DeniedBridge(){}
DeniedBridge.prototype.call=function(uri,data){
  deniedRequests.push({uri,data,bridge:this});
};
const deniedOverlay=environment('http://go2rtc.test:1984',null,null,{},DeniedBridge);
deniedOverlay.timers.find(x=>x.ms===2500).fn();
deniedRequests[0].bridge.onservicecallback('{"returnValue":false,"errorText":"private"}');
deniedOverlay.intervals.find(x=>x.ms===45000).fn();
assert.equal(deniedRequests.length,1,'denied system API is not called repeatedly');
const deniedStored=JSON.parse(deniedOverlay.saved['go2rtc-pip-lifecycle-v1']);
assert.equal(deniedStored.guardState,'denied','failure is captured in bounded diagnostics');
assert(!JSON.stringify(deniedStored).includes('private'),
  'Luna denial details are not persisted as secret-bearing strings');

const e=environment('http://go2rtc.test:1984');
assert.equal(e.xhr[0].url,'http://go2rtc.test:1984/api/webos-pip');
e.xhr[0].respond(200,{cameras:[{stream:'door',label:'Door',mjpeg_stream:'door_pip_mjpeg'},{stream:'hall',label:'Hall'},
   {stream:'door_pip_mjpeg'},{stream:'door_tv_aac'}],sound_enabled:false});
assert.equal(e.saved['go2rtc-pip-camera'],'door');
assert.equal(e.dom.empty.hidden,true);
const firstSnapshot=e.dom.tiles.children[0].children.find(
  x=>x.src.includes('/api/frame.jpeg?src=door_pip_mjpeg'));
assert(firstSnapshot,'first snapshot must reuse the existing MJPEG source, not H264 transcoding');
const persistent=e.dom.tiles.children[0].children.find(x=>x.id==='mjpeg');
assert(persistent && persistent.src.includes('/api/stream.mjpeg?src=door_pip_mjpeg'),
       'JPEG-only PiP must start an image stream, not video playback');
firstSnapshot.onload();
assert.equal(e.dom.statusLabel.textContent,'LIVE','first camera frame must display LIVE');
assert.equal(e.dom.status.attrs['data-state'],'live','LIVE badge must reflect actual frame status');
assert.equal(firstSnapshot.style.width,'480px','fill video frame horizontally');
assert.equal(firstSnapshot.style.height,'360px','crop 4:3 source into 16:9 card');
assert.equal(firstSnapshot.style.top,'-45px','center crop and remove black margins');
assert.equal(e.dom.video.src,'','hardware video player must stay unused');
e.events.keydown({keyCode:406,preventDefault(){}});
assert.equal(e.dom.menu.hidden,false);
assert(e.dom.menuItems.children[0].textContent.includes('Камера: door'),
       'menu lets user select a physical camera');
e.dom.menuItems.children[0].onclick();
assert.equal(e.saved['go2rtc-pip-camera'],'hall');
e.events.keydown({keyCode:403,preventDefault(){}});
assert.equal(e.dom.pip.attrs['data-private'],'true');
e.events.keydown({keyCode:461,preventDefault(){}});
assert.equal(e.closed(),0,'Back must not terminate the app');
e.events['win:webOSRelaunch']();
const last=e.xhr[e.xhr.length-1];
assert.equal(last.url,'http://go2rtc.test:1984/api/webos-pip');
last.respond(200,{cameras:[{stream:'garden',label:'Garden'}]});
assert.equal(e.saved['go2rtc-pip-camera'],'garden','removed camera replaced');
assert(!/door/.test(e.dom.menuItems.textContent||'') || e.saved['go2rtc-pip-camera']==='garden');
const callsBeforeSuspend=e.xhr.length;
e.doc.hidden=true;
e.events.visibilitychange();
assert.equal(persistent.hidden,true,'hidden app releases JPEG stream');
e.events['win:webOSRelaunch']();
assert.equal(e.xhr.length,callsBeforeSuspend,
  'a hidden app cannot restart requests even when relaunched');
e.doc.hidden=false;
e.events.webkitvisibilitychange();
assert.equal(e.xhr.length,callsBeforeSuspend+1,'visibility restore restarts catalog');
assert.equal(typeof e.events.webOSRelaunch==='function',true,
  'official document webOSRelaunch event must be handled');
e.dom.close.onclick();
assert.equal(e.closed(),1,'close must explicitly release app');

// Stock upstream may not have /api/webos-pip. Fall back immediately to the
// native streams list and resolve the matching MJPEG auxiliary source.
const stock=environment('http://go2rtc.test:1984');
stock.xhr[0].respond(404,{});
const nextTick=stock.timers[stock.timers.length-1];
assert.equal(nextTick.ms,0,'stock go2rtc fallback must have no 5s penalty');
nextTick.fn();
assert.equal(stock.xhr[1].url,'http://go2rtc.test:1984/api/streams');
stock.xhr[1].respond(200,{door:{},door_pip_mjpeg:{}});
assert(stock.dom.tiles.children[0].children.some(
  x=>x.src.includes('/api/stream.mjpeg?src=door_pip_mjpeg')),
  'native streams list must link camera to existing technical JPEG stream');

const initial=environment('');
assert.equal(initial.dom.setup.hidden,false,'missing server requires setup');
assert.equal(initial.dom.status.attrs['aria-label'],'Настройка камеры','setup state must preserve status accessibility');
assert.equal(initial.dom.statusLabel.textContent,'НАСТР','setup badge must show short text');
assert.equal(typeof initial.dom.menuButton.onclick,'function','... button must bind before setup');
assert.equal(typeof initial.dom.close.onclick,'function','close must bind before setup');
assert.equal(typeof initial.events.keydown,'function','remote events must bind before setup');
initial.events.keydown({keyCode:406,preventDefault(){}});
assert.equal(initial.dom.menu.hidden,false,'remote blue key opens menu even without URL');
initial.events.keydown({keyCode:461,preventDefault(){}});
assert.equal(initial.dom.menu.hidden,true,'Back closes menu');
initial.dom.serverAddress.value='http://go2rtc.local:1984';
initial.events.keydown({keyCode:13,target:initial.dom.serverSave,preventDefault(){}});
assert.equal(initial.saved['go2rtc-pip-server'],'http://go2rtc.local:1984');
assert.equal(initial.reloads(),1,'Enter on focused setup button must click, not open menu');
initial.dom.close.onclick();
assert.equal(initial.closed(),1,'close button must work before server is configured');

// Official LG Application Manager launch(params) is delivered on first boot
// and persisted in app-local storage; no write to the root-owned app files.
const paired=environment('',{go2rtcServer:'http://10.0.0.9:1984'});
assert.equal(paired.dom.setup.hidden,true,'paired TV launches without URL dialog');
assert.equal(paired.saved['go2rtc-pip-server'],'http://10.0.0.9:1984',
             'provisioned private endpoint persists in localStorage');
assert.equal(paired.xhr[0].url,'http://10.0.0.9:1984/api/webos-pip',
             'first camera discovery automatically uses launch params');

// The first image must start directly from verified launcher camera params:
// on stock go2rtc the catalog XHR from an installed file:// webOS app may
// fail CORS; the browser's standard <img> is not subject to XHR CORS checks.
const boot=environment('',{
  go2rtcServer:'http://10.0.0.9:1984',
  go2rtcCamera:'door',
  go2rtcMjpeg:'door_pip_mjpeg'
});
assert.equal(boot.dom.setup.hidden,true,'no manual URL dialog');
assert.equal(boot.saved['go2rtc-pip-camera'],'door');
assert.equal(boot.saved['go2rtc-pip-mjpeg'],'door_pip_mjpeg');
const direct=boot.dom.tiles.children[0].children;
assert(direct.some(x=>x.src.includes(
  'http://10.0.0.9:1984/api/stream.mjpeg?src=door_pip_mjpeg')),
  'direct MJPEG stream starts without catalog XMLHttpRequest completion');
assert(direct.some(x=>x.src.includes(
  'http://10.0.0.9:1984/api/frame.jpeg?src=door_pip_mjpeg')),
  'direct JPEG snapshot fallback starts without catalog XMLHttpRequest');
boot.xhr[0].respond(0,{});
assert(direct.some(x=>x.src.includes('stream.mjpeg?src=door_pip_mjpeg')),
  'failed cross-origin catalog must not cancel direct image transport');
assert.equal(boot.dom.status.attrs['aria-label'],'Подключение к камере','loading state must report connection progress');
assert.equal(boot.dom.statusLabel.textContent,'···','connecting status must stay compact');

// Some webOS builds only deliver launchParams via webOSLaunch after DOM load.
const delayed=environment('');
assert.equal(delayed.dom.setup.hidden,false);
delayed.events.webOSLaunch({detail:{go2rtcServer:'http://10.0.0.9:1984'}});
assert.equal(delayed.saved['go2rtc-pip-server'],'http://10.0.0.9:1984');
assert.equal(delayed.reloads(),1,'late launch parameters activate without typing');
const moved=environment('http://10.0.0.9:1984');
moved.events.webOSRelaunch({detail:{go2rtcServer:'http://10.0.0.10:1984'}});
assert.equal(moved.saved['go2rtc-pip-server'],'http://10.0.0.10:1984',
             'new LAN endpoint overrides stale saved URL on relaunch');
assert.equal(moved.reloads(),1);

// Never adopt a public endpoint, userinfo, arbitrary path or invalid octets.
for(const badUrl of [
  'http://8.8.8.8:1984',
  'http://user:secret@10.0.0.9:1984',
  'http://10.0.0.9:1984/private',
  'https://10.0.0.9:1984',
  'http://10.888.0.9:1984'
]){
  const invalid=environment('',{go2rtcServer:badUrl});
  assert.equal(invalid.dom.setup.hidden,false,'reject unsafe URL '+badUrl);
  assert.equal(invalid.xhr.length,0,'no request to unsafe server');
}

// 0.1.4: LG 55NANO926PB control, safe PTZ and opt-in audio behaviour.
const token='T'.repeat(48);
const live=environment('',{
  go2rtcServer:'http://10.0.0.9:1984',
  go2rtcCamera:'door',
  go2rtcMjpeg:'door_pip_mjpeg',
  go2rtcPTZToken:token,
  go2rtcAudio:'door_tv_aac'
});
live.xhr[0].respond(200,{cameras:[{stream:'door',mjpeg_stream:'door_pip_mjpeg',audio_stream:'door_tv_aac'}],
  ptz_enabled:true,ptz_camera:'door',sound_enabled:true});
const indicator=live.xhr.find(x=>x.url.includes('/api/webos-pip/activity'));
assert(indicator,'native activity requests must be sent');
indicator.respond(200,{state:'active'});
assert(!html.includes('id="soundText"'),'quiet and sound state never show text on the badge');
assert.equal(live.dom.sound.attrs['data-state'],'active');
assert.equal(live.dom.soundInfo.textContent,'Обнаружен звук');
assert.equal(live.dom.sound.attrs['aria-label'],'Обнаружен звук камеры');
assert(typeof live.dom.sound.onclick==='function','sound icon should be clickable');
assert.equal(live.dom.ptzButton.attrs['aria-expanded'],undefined,
  'PTZ overlay should not start expanded');
const initialRequests=live.xhr.length;
live.dom.ptzButton.onclick();
assert.equal(live.dom.ptzPanel.hidden,false);
assert.equal(live.dom.ptzButton.attrs['aria-expanded'],'true');
live.events.keydown({keyCode:38,preventDefault(){}});
assert.equal(live.xhr.length,initialRequests,
  'remote arrows must not turn the physical camera without OK');
const left=live.dom.ptzGrid.children.find(x=>x.attrs['data-dir']==='left');
left.click();
assert(live.dom.ptzHint.textContent.includes('поворачиваю камеру'),
  'first PTZ press must provide immediate visible progress');
assert.equal(live.dom.ptzPanel.attrs['aria-busy'],'true',
  'PTZ busy state must be exposed for screen readers');
const motorCount=live.xhr.length;
left.click();
assert.equal(live.xhr.length,motorCount,
  'repeated button press during motor movement must not double-command motor');
assert(live.dom.ptzHint.textContent.includes('предыдущая команда'),
  'repeated PTZ press must visibly acknowledge the busy state');
const slowPTZ=live.timers.find(x=>x.ms===1200);
assert(slowPTZ,'must schedule delayed ONVIF progress notice');
slowPTZ.fn();
assert(live.dom.ptzHint.textContent.includes('ONVIF-ответ'),
  'slow PTZ must explain that the camera has not answered yet');
const motor=live.xhr[live.xhr.length-1];
assert.equal(motor.method,'POST');
assert.equal(motor.url,'http://10.0.0.9:1984/api/webos-pip/ptz');
assert.equal(motor.headers['X-Go2RTC-PTZ'],token);
assert.equal(JSON.parse(motor.data).direction,'left');
motor.respond(200,{ok:true});
assert(live.dom.ptzHint.textContent.includes('готово за'),
  'confirmed PTZ must show elapsed time');
assert.equal(live.dom.ptzPanel.attrs['aria-busy'],'false',
  'PTZ completion must restore an interactive state');
const completedPTZCount=live.xhr.length;
left.click();
assert.equal(live.xhr.length,completedPTZCount,
  'quick repeated remote click after SOAP completion must not issue another motor step');
live.events.keydown({keyCode:461,preventDefault(){}});
assert.equal(live.dom.ptzPanel.hidden,true,'Back closes only PTZ overlay');

live.dom.sound.onclick();
assert.equal(live.dom.soundPanel.hidden,false,'sound icon opens sound actions');
assert.equal(live.dom.cameraAudio.preload,'auto','opening audio panel prebuffers AAC without playing');
assert.equal(live.dom.cameraAudio.src,
  'http://10.0.0.9:1984/api/stream.aac?src=door_tv_aac');
assert.equal(live.dom.sound.attrs['data-state'],'active',
  'activity indicator is independent of playback');
live.dom.soundListen.click();
assert.equal(live.dom.cameraAudio.src,
  'http://10.0.0.9:1984/api/stream.aac?src=door_tv_aac',
  'audio must be opt-in and reuse native go2rtc AAC endpoint');
live.dom.soundListen.click();
assert.equal(live.dom.cameraAudio.src,'','stop listening releases audio');
live.dom.soundListen.click();
assert.equal(live.dom.audioInfo.textContent,'Подключение к AAC…');
live.dom.cameraAudio.onerror();
assert(live.dom.audioInfo.textContent.includes('ошибка плеера 0'),
  'audio errors remain legible after the short toast');
// Real remote Enter auto-repeat can outlive a fast SOAP response.
const heldClock={now:10000};
const held=environment('',{
  go2rtcServer:'http://10.0.0.9:1984',
  go2rtcCamera:'door',
  go2rtcMjpeg:'door_pip_mjpeg',
  go2rtcPTZToken:token
},heldClock);
held.xhr[0].respond(200,{cameras:[{stream:'door',mjpeg_stream:'door_pip_mjpeg'}],
  ptz_enabled:true,ptz_camera:'door'});
held.dom.ptzButton.onclick();
const heldLeft=held.dom.ptzGrid.children.find(x=>x.attrs['data-dir']==='left');
const heldRight=held.dom.ptzGrid.children.find(x=>x.attrs['data-dir']==='right');
const heldStop=held.dom.ptzGrid.children.find(x=>x.attrs['data-dir']==='stop');
held.events.keydown({keyCode:13,target:heldLeft,preventDefault(){}});
const heldFirst=held.xhr[held.xhr.length-1];
assert.equal(JSON.parse(heldFirst.data).direction,'left');
heldFirst.respond(200,{ok:true});
heldClock.now+=2200;
const afterFirst=held.xhr.length;
held.events.keydown({keyCode:13,target:heldLeft,repeat:true,preventDefault(){}});
held.events.keydown({keyCode:13,target:heldLeft,preventDefault(){}});
assert.equal(held.xhr.length,afterFirst,
  'held Enter never starts more than one PTZ step even after throttle expires');
assert(typeof held.events.keyup==='function','remote keyup must rearm deliberate PTZ');
held.events.keyup({keyCode:13});
held.events.keydown({keyCode:13,target:heldRight,preventDefault(){}});
assert.equal(held.xhr.length,afterFirst+1,'released Enter permits next deliberate step');
const heldMotor=held.xhr[held.xhr.length-1];
assert.equal(JSON.parse(heldMotor.data).direction,'right');
heldStop.click();
const emergency=held.xhr[held.xhr.length-1];
assert.equal(JSON.parse(emergency.data).direction,'stop',
  'emergency stop must bypass busy motor lock and keep token authentication');
assert.equal(emergency.headers['X-Go2RTC-PTZ'],token,
  'emergency stop remains scoped to the TV credential');
heldStop.click();
assert.equal(held.xhr[held.xhr.length-1],emergency,
  'held emergency stop is rate-limited without starving motor stop');

const unpaired=environment('',{go2rtcServer:'http://10.0.0.9:1984'});
unpaired.xhr[0].respond(200,{cameras:[{stream:'door',mjpeg_stream:'door_pip_mjpeg'}],
  ptz_enabled:true,ptz_camera:'door',sound_enabled:false});
unpaired.dom.ptzButton.click();
const leftNoToken=unpaired.dom.ptzGrid.children.find(x=>x.attrs['data-dir']==='left');
assert.equal(leftNoToken.disabled,true,'no server token means disabled motor');
assert(unpaired.dom.ptzHint.textContent.includes('ключ телевизора не передан'),
  'PTZ missing token must be visible');
const beforeUnauthorized=unpaired.xhr.length;
leftNoToken.click();
assert.equal(unpaired.xhr.length,beforeUnauthorized,'never send unauthenticated PTZ');
const style=html.slice(html.indexOf('<style>'),html.indexOf('</style>'));
assert(/#pip\[data-size=small\] #menu\s*\{[^}]*max-height:calc/.test(style),
  'small PiP must have an adaptive scrollable menu');
// Match the owner's supplied 880×560 artboard, not an improvised theme.
assert(style.includes('--glass:rgba(14,17,23,.62)') &&
       style.includes('--stroke:rgba(255,255,255,.28)'),
  'glass and border colors must match the supplied artboard');
assert(style.includes('border:1.5px solid var(--stroke)'),
  'main controls must use the supplied 1.5px white translucent stroke');
assert(style.includes('#toolbar {position:absolute;top:20px;right:20px') &&
       style.includes('gap:4px;padding:4px'),
  'toolbar must match the mock position, spacing and interior padding');
assert(style.includes('.tool {width:44px;height:44px;flex:0 0 44px') &&
       style.includes('.tool svg {width:22px;height:22px'),
  'toolbar controls and icons must match 44/22px artboard dimensions');
assert(style.includes('#status {left:20px;height:36px;gap:0;padding:0 16px 0 14px') &&
       style.includes('#sound {right:20px;bottom:20px;width:44px;height:44px'),
  'LIVE must be lower-left and sound lower-right as in the source');
assert(style.includes('#status[data-state=live]::before {background:#3DDC84') &&
       style.includes('animation:livePulse 2s ease-in-out infinite'),
  'LIVE indicator uses the exact green and pulse of the supplied design');
assert(style.includes('#sound[data-state=active] {color:#ff596b}') &&
       style.includes('#sound[data-state=quiet],#sound[data-state=unavailable] {color:#aeb5bf}'),
  'detected camera noise is red, otherwise gray, independent of AAC listening');
assert(style.includes('#pip[data-size=small] #status {left:8px') &&
       style.includes('#pip[data-size=small] #sound {right:8px'),
  'small PiP must preserve left/right positions without clipping');
assert(style.includes('#statusLabel {display:inline-block;margin-left:12px') &&
       style.includes('#pip[data-size=small] #statusLabel {margin-left:9px'),
  'LIVE label must have explicit spacing in legacy webOS engines without flex gap');
assert(style.includes('#pip[data-size=large] #menu {bottom:auto;max-height:calc(100% - 100px);overflow-y:auto}'),
  'large PiP uses natural menu height and only scrolls if content exceeds screen');
assert(style.includes('#menu {position:absolute;') &&
       style.includes('overflow-y:auto;-webkit-overflow-scrolling:touch'),
  'small and medium PiP retain navigable scrolling');
assert(style.includes('#menu::-webkit-scrollbar') &&
       style.includes('#menu::-webkit-scrollbar-thumb') &&
       style.includes('background:rgba(206,219,233,.48)'),
  'webOS scrollbar must be slim and glass-styled rather than system gray');
assert(style.includes('#diagPanel::-webkit-scrollbar {width:6px;height:6px}') &&
       style.includes('#diagPanel::-webkit-scrollbar-track {background:rgba(255,255,255,.045)') &&
       style.includes('#diagPanel::-webkit-scrollbar-thumb {background:rgba(206,219,233,.48)') &&
       style.includes('#diagPanel::-webkit-scrollbar-thumb:hover {background:rgba(206,219,233,.76)'),
  'diagnostic dialog must have the same slim glass scrollbar as the other panels');
assert(style.includes('#ptzPanel,#diagPanel {scrollbar-width:thin;scrollbar-color:rgba(206,219,233,.48) transparent}'),
  'diagnostics must use the matching thin scrollbar also on newer CSS engines');
assert(html.includes('id="sound" class="event-badge"') &&
       html.includes('id="status" class="event-badge"'),
  'status and sound must share the same glass surface component');
assert(html.includes('id="menuButton"') &&
       html.includes('<circle cx="5" cy="12" r="1.8"/>') &&
       html.includes('<circle cx="19" cy="12" r="1.8"/>'),
  'menu ellipsis must use the mock SVG rather than a font glyph');
assert(html.includes('M4 9.5h3.5L12 6v12l-4.5-3.5H4z'),
  'camera sound icon path must match the supplied artwork');
assert(html.includes('id="statusLabel"') &&
       html.includes('id="status" class="event-badge" data-state="connecting"'),
  'LIVE badge must show a text label inside the shared glass component');
assert(client.includes("state==='live'?'LIVE'"),
  'a live connection must show LIVE text in the badge');
assert(!client.includes('status.textContent='),
  'state changes must update only the child label and preserve badge styling');
const toolbar=html.indexOf('id="toolbar"'),ptz=html.indexOf('id="ptzButton"'),
  menu=html.indexOf('id="menuButton"');
assert(toolbar>=0&&ptz>toolbar&&ptz<menu,
  'PTZ is part of the same rounded upper toolbar, not a floating control');
assert(html.indexOf('id="status"')>html.indexOf('id="close"'),
  'LIVE must not fade with the upper toolbar');
const detected=environment('',{go2rtcServer:'http://10.0.0.9:1984'});
detected.xhr[0].respond(404,{});
detected.timers[detected.timers.length-1].fn();
detected.xhr[1].respond(200,{door:{},door_tv_aac:{}});
detected.dom.soundListen.click();
assert.equal(detected.dom.cameraAudio.src,
 'http://10.0.0.9:1984/api/stream.aac?src=door_tv_aac',
 'stock catalog can discover declared paired AAC without a launch parameter');
assert.equal(detected.dom.sound.attrs['data-state'],'unavailable',
 'no sound activity API must never appear as false silence');
detected.dom.soundListen.click();
assert.equal(detected.dom.cameraAudio.src,'','pending AAC may be cancelled');

assert(!/soundText\.textContent\s*=/.test(client),
  'no TИХО or active listening label in the always-on microphone indicator');
assert(!/setTimeout\s*\(\s*close\s*,\s*300000/.test(client),
  'application must not schedule a five-minute automatic close');

// A stalled multipart image gets a low-rate JPEG health probe, then falls
// back to normal JPEG after two consecutive producer failures.
const healthClock={now:1000};
const heal=environment('http://go2rtc.test:1984',null,healthClock);
heal.xhr[0].respond(200,{cameras:[{stream:'door',mjpeg_stream:'door_pip_mjpeg'}]});
const healMJPEG=heal.created.find(x=>x.id==='mjpeg');
healMJPEG.onload();
const healthTick=heal.intervals.find(x=>x.ms===1000);
assert(healthTick,'media watchdog must run periodically');
healthClock.now=22000;healthTick.fn();
const probe1=heal.created.find(x=>x.src.includes('_health=22000'));
assert(probe1,'multipart health-check uses only one JPEG probe');
probe1.onerror();
assert.equal(healMJPEG.hidden,false,'one transient probe failure must not drop the live image');
healthClock.now=44000;healthTick.fn();
const probe2=heal.created.find(x=>x.src.includes('_health=44000'));
assert(probe2,'second bounded health probe occurs after 20 seconds');
probe2.onerror();
assert.equal(healMJPEG.hidden,true,'two failed probes trigger bounded JPEG fallback');
assert(heal.created.some(x=>x.src.includes('/api/frame.jpeg?src=door_pip_mjpeg')),
  'loss of multipart transport restores the standard camera frame');

// High quality is opt-in, advertised by the server, and uses JPEG images only.
const q=environment('http://go2rtc.test:1984');
q.xhr[0].respond(200,{cameras:[{stream:'door',mjpeg_stream:'door_pip_mjpeg',mjpeg_max_stream:'door_pip_max_mjpeg'}],sound_enabled:false});
q.events.keydown({keyCode:406,preventDefault(){}});
const qualityItem=q.dom.menuItems.children.find(x=>x.textContent==='Качество: обычное');
assert(qualityItem,'offer max quality only when a separate alias exists');
qualityItem.onclick();
assert.equal(q.saved['go2rtc-pip-quality'],'max','quality choice persisted');
assert(q.dom.tiles.children[0].children.some(x=>x.src.includes('stream.mjpeg?src=door_pip_max_mjpeg')),
  'max quality uses a validated dedicated MJPEG source');
assert(q.dom.tiles.children[0].children.some(x=>x.src.includes('frame.jpeg?src=door_pip_max_mjpeg')),
  'JPEG fallback follows quality choice');
const low=environment('http://go2rtc.test:1984');
low.xhr[0].respond(200,{cameras:[{stream:'door',mjpeg_stream:'door_pip_mjpeg'}],sound_enabled:false});
low.events.keydown({keyCode:406,preventDefault(){}});
assert(!low.dom.menuItems.children.some(x=>x.textContent.includes('Качество:')),
  'no fictitious high quality when only low-resolution stream exists');

// A real reboot/termination of webOS cannot be intercepted reliably. Store only
// bounded event names and elapsed seconds, then display on the next launch.
const storedLifecycle={};
const diagClock={now:1000};
const before=environment('http://go2rtc.test:1984',null,diagClock,storedLifecycle);
assert(before.intervals.some(x=>x.ms===5000),'lifecycle heartbeat must persist regularly');
before.xhr[0].respond(200,{cameras:[{stream:'door',mjpeg_stream:'door_pip_mjpeg'}]});
const firstFrame=before.created.find(x=>x.src.includes('/api/frame.jpeg?src=door_pip_mjpeg'));
firstFrame.onload();
diagClock.now=121000;
before.intervals.find(x=>x.ms===5000).fn();
const afterTick=JSON.parse(storedLifecycle['go2rtc-pip-lifecycle-v1']);
assert.equal(afterTick.last,121000,'persisted heartbeat reaches 120 seconds');
assert.equal(afterTick.lastEvent,'first-frame','heartbeats do not overwrite lifecycle event');
before.doc.hidden=true;
before.events.visibilitychange();
const invisible=JSON.parse(storedLifecycle['go2rtc-pip-lifecycle-v1']);
assert.equal(invisible.lastEvent,'hidden','webOS hidden event saved before suspension');
assert(invisible.events.length<=12,'local event history is bounded');
const after=environment('http://go2rtc.test:1984',null,{now:140000},storedLifecycle);
after.events.keydown({keyCode:406,preventDefault(){}});
const diagnostic=after.dom.menuItems.children.find(x=>x.textContent==='Диагностика окна');
assert(diagnostic,'diagnostics accessible through remote settings');
diagnostic.onclick();
assert.equal(after.dom.diagPanel.hidden,false,'diagnostic dialog visible');
assert(after.dom.diagText.textContent.includes('Длительность: 120 с'),
       'next launch shows previous session duration');
assert(after.dom.diagText.textContent.includes('hidden'),
       'next launch shows last lifecycle event');
assert(after.dom.diagText.textContent.includes('webOS сообщила о скрытии окна'),
       'next launch classifies observed hide without claiming a crash');
assert(after.dom.diagText.textContent.includes('JPEG исходник:'),
       'diagnostics show the decoder-reported JPEG dimensions');
after.events.keydown({keyCode:461,preventDefault(){}});
assert.equal(after.dom.diagPanel.hidden,true,'LG Back closes diagnostic without exiting app');
assert.equal(after.closed(),0,'diagnostic never closes the PiP itself');
const persisted=JSON.parse(storedLifecycle['go2rtc-pip-lifecycle-v1']);
assert(!JSON.stringify(persisted).includes('go2rtc.test'),'diagnostics do not record server addresses');
after.events['win:error']({message:'private-token-value'});
const safeError=JSON.stringify(JSON.parse(storedLifecycle['go2rtc-pip-lifecycle-v1']));
assert(safeError.includes('js-error')&&!safeError.includes('private-token-value'),
       'JS error logs event type only; no exception details');

const silentStorage={};
const silent=environment('http://go2rtc.test:1984',null,{now:3000},silentStorage);
const reopened=environment('http://go2rtc.test:1984',null,{now:125000},silentStorage);
reopened.events.keydown({keyCode:406,preventDefault(){}});
reopened.dom.menuItems.children.find(x=>x.textContent==='Диагностика окна').onclick();
assert(reopened.dom.diagText.textContent.includes('start'),
       'unannounced process disappearance retains the last stored event');
assert(reopened.dom.diagText.textContent.includes('не доказывает'),
       'absence of unload event is never treated as proof of OS kill');
assert(reopened.dom.diagText.textContent.includes('возможна остановка webOS'),
       'missing unload event is labeled uncertain, never reported as a proven kill');
assert(silent.intervals.find(x=>x.ms===5000),'silent test starts heartbeat');

// 0.1.17: 210-second unattended test with the diagnostic dialog OPEN.
// The TV may remove an overlay without dispatching pagehide/visibilitychange;
// preserve the last heartbeat, frame observation and panel state across relaunches.
const idleStorage={},idleClock={now:1000};
const idle=environment('http://go2rtc.test:1984',null,idleClock,idleStorage);
idle.xhr[0].respond(200,{cameras:[{stream:'door',mjpeg_stream:'door_pip_mjpeg'}]});
const idleFirst=idle.created.find(x=>x.src.includes('/api/frame.jpeg?src=door_pip_mjpeg'));
idleFirst.onload();
const idleMultipart=idle.created.find(x=>x.id==='mjpeg');
idleMultipart.onload();
idle.events.keydown({keyCode:406,preventDefault(){}});
idle.dom.menuItems.children.find(x=>x.textContent==='Диагностика окна').onclick();
assert.equal(idle.dom.diagPanel.hidden,false,'210s reproduction keeps diagnostics open');
idleClock.now=211000;
idle.intervals.find(x=>x.ms===5000).fn();
const idleEvidence=JSON.parse(idleStorage['go2rtc-pip-lifecycle-v1']);
assert.equal(idleEvidence.last,211000,'heartbeat persists at 210s');
assert.equal(idleEvidence.imageAge,210,'last confirmed frame age is recorded separately');
assert.equal(idleEvidence.frameMode,'mjpeg','last observed transport is recorded');
assert.equal(idleEvidence.panelOpen,true,'diagnostic panel open at last heartbeat');
assert.equal(idleEvidence.visibility,'visible','last known visibility persists');
assert.equal(idleEvidence.lastEvent,'first-frame','no invented pagehide/close event');
const openedAfter210=environment('http://go2rtc.test:1984',null,{now:221000},idleStorage);
openedAfter210.events.keydown({keyCode:406,preventDefault(){}});
openedAfter210.dom.menuItems.children.find(x=>x.textContent==='Диагностика окна').onclick();
const recoveryText=openedAfter210.dom.diagText.textContent;
assert(recoveryText.includes('Длительность: 210 с'),
       'real 210s duration remains visible at next launch');
assert(recoveryText.includes('Диагностика была открыта: да'),
       'open dialog state stays visible after app disappearance');
assert(recoveryText.includes('Возраст подтверждённого кадра¹: 210 с'),
       'frame observation retained, not treated as server error');
assert(recoveryText.includes('нет события закрытия'),
       'no invisible webOS event fabricated');
assert(recoveryText.includes('стороннее webOS overlay, не системный LG Multi-View'),
       'app does not misrepresent itself as native LG Multi-View');
assert(!/go2rtc.test|192[.]168|private-token-value/.test(
  idleStorage['go2rtc-pip-lifecycle-history-v2']),
  'only bounded metadata is archived, not endpoint or credentials');
const openedAgain=environment('http://go2rtc.test:1984',null,{now:241000},idleStorage);
openedAgain.events.keydown({keyCode:406,preventDefault(){}});
openedAgain.dom.menuItems.children.find(x=>x.textContent==='Диагностика окна').onclick();
assert(openedAgain.dom.diagText.textContent.includes('Запуск -1: 210 с'),
       'previous 210s run remains inspectable after TWO later launches');
const hostileStorage={'go2rtc-pip-lifecycle-history-v2':JSON.stringify([
  {v:1,started:1000,last:211000,lastEvent:'first-frame',
   events:[[0,'start']],private:'private-token-value'}
])};
environment('http://go2rtc.test:1984',null,{now:310000},hostileStorage);
assert(!hostileStorage['go2rtc-pip-lifecycle-history-v2'].includes('private-token-value'),
       'malformed history cannot smuggle arbitrary private fields');

console.log('go2rtc webOS PiP 0.1.17: 210s lifecycle history and MultiView boundary PASS');
