/* go2rtc LG webOS PiP: JPEG-only camera overlay (multipart MJPEG + snapshot fallback), no MSE/video decoder. */
(function () {
  'use strict';
  var cfg=window.PIP_CONFIG||{}, storedUrl='';
  try { storedUrl=localStorage.getItem('go2rtc-pip-server')||''; } catch(e) {}
  // Old LG Developer Mode cannot overwrite files of installed applications.
  // The trusted MCPshka launcher instead sends a private LAN API endpoint
  // through standard webOSLaunch/webOSRelaunch parameters.
  function validLaunchServer(value) {
    if(typeof value!=='string')return '';
    var m=/^http:\/\/(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3}):(\d{2,5})$/.exec(value);
    if(!m)return '';
    var a=[Number(m[1]),Number(m[2]),Number(m[3]),Number(m[4])];
    var port=Number(m[5]),i;
    for(i=0;i<4;i++)if(a[i]>255||String(a[i])!==m[i+1])return '';
    if(port<1024||port>65535)return '';
    if(!(a[0]===10||(a[0]===172&&a[1]>=16&&a[1]<=31)||
         (a[0]===192&&a[1]===168)))return '';
    return value;
  }
  // Direct mode is intentionally limited to credential-free HTTP image URLs
  // on a private IPv4 camera. RTSP, URLs with query tokens, and internet hosts
  // require a trusted go2rtc instance rather than the webOS <img> element.
  function validDirectCameraURL(value) {
    if(typeof value!=='string'||value.length>240)return '';
    var m=/^https?:\/\/(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?::(\d{1,5}))?(\/[A-Za-z0-9_./-]{1,180})$/.exec(value);
    if(!m)return '';
    var a=[Number(m[1]),Number(m[2]),Number(m[3]),Number(m[4])];
    for(var i=0;i<4;i++)if(a[i]>255||String(a[i])!==m[i+1])return '';
    if(!(a[0]===10||(a[0]===172&&a[1]>=16&&a[1]<=31)||(a[0]===192&&a[1]===168)))return '';
    if(m[5]){var port=Number(m[5]);if(port<1||port>65535||String(port)!==m[5])return '';}
    if(m[6].indexOf('..')>=0||!/(?:\.mjpg|\.mjpeg|\/mjpeg|\.jpg|\.jpeg|\/snapshot)$/i.test(m[6]))return '';
    return value;
  }
  function isDirectMultipart(value){return /(?:\.mjpg|\.mjpeg|\/mjpeg)$/i.test(value);}

  function launchParameters(event) {
    var data=event&&event.detail;
    if(!data) {
      try {
        data=(window.PalmSystem&&window.PalmSystem.launchParams)||
             (window.webOSSystem&&window.webOSSystem.launchParams)||
             window.launchParams;
      } catch(e) {}
    }
    if(typeof data==='string'){
      try{data=JSON.parse(data);}catch(e){return {};}
    }
    if(data&&data.params&&!data.go2rtcServer)data=data.params;
    return data&&typeof data==='object'?data:{};
  }
  function serverFromLaunch(event) {
    return validLaunchServer(launchParameters(event).go2rtcServer);
  }
  function ptzTokenFromLaunch(event) {
    var data=launchParameters(event),token=data.go2rtcPTZToken;
    return typeof token==='string' && /^[A-Za-z0-9_-]{32,128}$/.test(token)?token:'';
  }
  function audioStreamFromLaunch(event) {
    var data=launchParameters(event),stream=data.go2rtcAudio;
    return typeof stream==='string' &&
      /^[A-Za-z0-9][A-Za-z0-9_.-]{0,98}_tv_aac$/.test(stream)?stream:'';
  }
  function cameraFromLaunch(event) {
    var data=launchParameters(event),name=data.go2rtcCamera,aux=data.go2rtcMjpeg;
    if(typeof name!=='string'||!/^[a-z0-9][a-z0-9_.-]{0,98}$/i.test(name)||
       /(_pip_mjpeg|_pip_max_mjpeg|_tv_aac)$/.test(name))return {camera:'',mjpeg:''};
    if(typeof aux!=='string'||aux!==name+'_pip_mjpeg')aux='';
    return {camera:name,mjpeg:aux};
  }
  var launchServer=serverFromLaunch(null),launchSelection=cameraFromLaunch(null);
  var newPTZToken=ptzTokenFromLaunch(null),newAudioStream=audioStreamFromLaunch(null);
  if(newPTZToken)try{localStorage.setItem('go2rtc-pip-ptz-token',newPTZToken);}catch(e){}
  if(newAudioStream)try{localStorage.setItem('go2rtc-pip-audio-stream',newAudioStream);}catch(e){}
  if(launchSelection.camera){
    try {
      localStorage.setItem('go2rtc-pip-camera',launchSelection.camera);
      if(launchSelection.mjpeg)localStorage.setItem('go2rtc-pip-mjpeg',launchSelection.mjpeg);
    }catch(e){}
  }
  if(launchServer) {
    storedUrl=launchServer;
    try{localStorage.setItem('go2rtc-pip-server',launchServer);}catch(e){}
  }
  var fromPage=(window.location && /^https?:$/.test(window.location.protocol)) ?
    (window.location.origin||window.location.protocol+'//'+window.location.host) : '';
  var base=String(cfg.url||storedUrl||fromPage).replace(/\/+$/,'');
  var directCameraURL=validDirectCameraURL(base),directMJPEG=isDirectMultipart(directCameraURL);
  var api=base+'/api/webos-pip', soundUrl=base?base+'/api/webos-pip/activity':'', cameras=[], mjpegSources={}, maxMJPEGSources={};
  var pip=document.getElementById('pip'), frame=document.getElementById('frame');
  var empty=document.getElementById('empty');
  var status=document.getElementById('status'), sound=document.getElementById('sound');
  var soundPanel=document.getElementById('soundPanel'),
      soundMonitor=document.getElementById('soundMonitor'),
      soundListen=document.getElementById('soundListen'),
      cameraAudio=document.getElementById('cameraAudio'),
      soundInfo=document.getElementById('soundInfo'),
      audioInfo=document.getElementById('audioInfo'),menu=document.getElementById('menu');
  var ptzButton=document.getElementById('ptzButton'),
      ptzPanel=document.getElementById('ptzPanel'),ptzGrid=document.getElementById('ptzGrid'),
      ptzHint=document.getElementById('ptzHint');
  var menuItems=document.getElementById('menuItems'), menuButton=document.getElementById('menuButton');
  var diagPanel=document.getElementById('diagPanel'),
      diagText=document.getElementById('diagText'),
      diagClose=document.getElementById('diagClose'),diagOpen=false;
  var closeButton=document.getElementById('close'), toast=document.getElementById('toast');
  var sizes=['small','medium','large'], corners=['tr','br','bl','tl'];
  var size='medium',corner='tr',fit='cover',mode='jpeg',quality='standard',full=false,privacy=false;
  var chosen='',ready=false,active=true,suspended=false,menuOpen=false,controlsTimer=null;
  var ptzOpen=false,soundOpen=false,audioPlaying=false,audioLoading=false,ptzBusy=false;
  // TV remotes can repeat held Enter even after a quick SOAP response.
  // Require a key release for another D-pad step, and throttle direct clicks.
  var ptzEnterHeld=false,ptzLastMoveAt=-100000,ptzLastStopAt=-100000;
  // Camera control is usable only if the server advertises its guarded
  // webOS endpoints AND the launcher supplied this device's private token.
  var ptzServer=false,ptzCamera='',soundMonitorAvailable=false,audioRequestId=0,audioTimer=null;
  var ptzToken=preferenceSafe('ptz-token'),audioStream=preferenceSafe('audio-stream');
  function preferenceSafe(key){try{return localStorage.getItem('go2rtc-pip-'+key)||'';}catch(e){return '';}}
  var jpgTimer=null,jpgDeadline=null,discoveryTimer=null,soundTimer=null,toastTimer=null,watchdog=null;
  var monitorRequest=null,catalogRequest=null,mjpegDeadline=null,mjpegPoll=null;
  var requestSeq=0,lastFrameAt=0;
  var successes=0,failures=0;
  var mjpegPending=false,mjpegActive=false,mjpegGeneration=0,mjpegFailures=0,mjpegRetryAt=0,mjpegRetryTimer=null;
  var mjpegHealthImage=null,mjpegHealthDeadline=null,mjpegLastProbe=0,mjpegProbeFailures=0;
  // One image request at a time, with bounded interval and backoff. Never
  // allocate a second hardware video decoder while TV playback is in progress.
  var pollMs=Math.max(100,Math.min(1200,Number(cfg.refresh_ms)||160));
  var timeoutMs=Math.max(1500,Math.min(9000,Number(cfg.timeout_ms)||4500));
  var catalogMs=Math.max(5000,Math.min(120000,Number(cfg.catalog_poll_ms)||15000));
  var secondary=document.createElement('img'),current=frame,next=secondary;
  // Reuse an existing go2rtc MJPEG technical stream instead of transcoding
  // H264 keyframes on *every* JPEG snapshot in a loop.
  var mjpegImage=document.createElement('img');
  mjpegImage.id='mjpeg';mjpegImage.alt=frame.alt;mjpegImage.draggable=false;
  mjpegImage.hidden=true;frame.parentNode.appendChild(mjpegImage);
  secondary.alt=frame.alt;secondary.draggable=false;secondary.hidden=true;
  frame.parentNode.appendChild(secondary);
  // 0.1.15: local-only, bounded webOS window lifecycle evidence.
  // No camera URL, IP, PTZ token or video pixels are collected/transmitted.
  var diagKey='go2rtc-pip-lifecycle-v1',
      diagArchiveKey='go2rtc-pip-lifecycle-history-v2',
      diagPrevious=null,diagArchive=[],diagStart=Date.now();
  var diagSession={v:1,started:diagStart,last:diagStart,lastEvent:'start',
                   events:[[0,'start']]};
  var diagFirstFrame=false;
  // webOS surface manager can close an untouched overlay at its own idle
  // deadline. Request a bounded per-window timer restart; never synthesize
  // remote keys, alter system configuration or restart foreground video.
  var guardState='pending',guardPingCount=0,guardLastOk=0;
  var guardPending=false,guardDisabled=false,guardDeadline=null,guardBridge=null;
  var lastRemoteActivityAt=diagStart,mjpegProducerLastOk=0;
  try {
    var oldDiag=localStorage.getItem(diagKey);
    if(oldDiag && oldDiag.length<4096){
      var parsedDiag=JSON.parse(oldDiag);
      if(parsedDiag && parsedDiag.v===1 &&
         typeof parsedDiag.started==='number' && typeof parsedDiag.last==='number' &&
         typeof parsedDiag.lastEvent==='string' && Array.isArray(parsedDiag.events)){
        diagPrevious=parsedDiag;
      }
    }
  } catch(e) {}
  // Keep up to five prior sessions, even after the viewer relaunches twice.
  // Permit only known event names and numbers; never archive arbitrary objects
  // that could contain private launch parameters or camera credentials.
  function safeDiagHistory(s){
    if(!s||s.v!==1||typeof s.started!=='number'||!isFinite(s.started)||
       typeof s.last!=='number'||!isFinite(s.last)||s.last<s.started||
       s.last-s.started>86400000||!Array.isArray(s.events))return null;
    var allowed=/^(start|first-frame|hidden|visible|pagehide|beforeunload|manual-close|relaunch|js-error|promise-error)$/;
    var events=[];
    for(var j=Math.max(0,s.events.length-12);j<s.events.length;j++){
      var item=s.events[j];
      if(Array.isArray(item)&&item.length===2&&typeof item[0]==='number'&&
         item[0]>=0&&item[0]<=86400&&allowed.test(item[1]))events.push([Math.floor(item[0]),item[1]]);
    }
    return {v:1,started:s.started,last:s.last,
      lastEvent:allowed.test(s.lastEvent)?s.lastEvent:'start',events:events,
      imageAge:typeof s.imageAge==='number'&&s.imageAge>=0&&s.imageAge<=86400?
        Math.floor(s.imageAge):null,
      frameMode:/^(mjpeg|snapshots|jpeg-fallback|connecting)$/.test(s.frameMode)?s.frameMode:'unknown',
      panelOpen:s.panelOpen===true,visibility:s.visibility==='hidden'?'hidden':'visible',
      guardState:/^(pending|waiting|ok|denied|unavailable|timeout|unknown)$/.test(s.guardState)?
        s.guardState:'unknown',
      guardCount:typeof s.guardCount==='number'&&s.guardCount>=0&&s.guardCount<=10000?
        Math.floor(s.guardCount):0,
      userIdle:typeof s.userIdle==='number'&&s.userIdle>=0&&s.userIdle<=86400?
        Math.floor(s.userIdle):null};
  }
  try{
    var historyData=localStorage.getItem(diagArchiveKey),rawHistory=[];
    if(historyData&&historyData.length<12000){
      var foundHistory=JSON.parse(historyData);
      if(Array.isArray(foundHistory))rawHistory=foundHistory.slice(0,5);
    }
    for(var h=0;h<rawHistory.length;h++){
      var oldHistory=safeDiagHistory(rawHistory[h]);
      if(oldHistory)diagArchive.push(oldHistory);
    }
    if(diagPrevious){
      var safePrevious=safeDiagHistory(diagPrevious);
      diagPrevious=safePrevious;
      if(safePrevious&&(!diagArchive.length||diagArchive[0].started!==safePrevious.started))
        diagArchive.unshift(safePrevious);
    }
    diagArchive=diagArchive.slice(0,5);
    localStorage.setItem(diagArchiveKey,JSON.stringify(diagArchive));
  }catch(e){}
  function diagCapture(){
    diagSession.imageAge=lastFrameAt?diagSeconds(Date.now(),lastFrameAt):null;
    diagSession.frameMode=mjpegActive?'mjpeg':mode==='snapshots'?'snapshots':lastFrameAt?'jpeg-fallback':'connecting';
    diagSession.panelOpen=!!diagOpen;
    diagSession.visibility=isHidden()?'hidden':'visible';
    diagSession.guardState=guardState;
    diagSession.guardCount=guardPingCount;
    diagSession.userIdle=diagSeconds(Date.now(),lastRemoteActivityAt);
  }
  function diagPersist(){
    diagCapture();
    try{localStorage.setItem(diagKey,JSON.stringify(diagSession));}catch(e){}
  }
  function diagSeconds(at,started){
    return Math.max(0,Math.min(86400,Math.floor((at-started)/1000)||0));
  }
  function guardText(state,count){
    if(state==='ok')return 'ответ получен ('+count+'); реальная защита проверяется';
    if(state==='denied')return 'webOS отклонила запрос';
    if(state==='timeout')return 'webOS не ответила';
    if(state==='unavailable')return 'API недоступен';
    if(state==='unknown')return 'раньше не измерялся';
    return 'ожидание ответа';
  }
  function finishOverlayGuard(reply){
    if(!guardPending)return;
    guardPending=false;guardBridge=null;
    if(guardDeadline!==null){clearTimeout(guardDeadline);guardDeadline=null;}
    var data=reply;
    if(typeof data==='string'){
      try{data=JSON.parse(data);}catch(e){data=null;}
    }
    if(data&&data.returnValue===true){
      guardState='ok';guardPingCount++;guardLastOk=Date.now();
    }else{
      guardState=reply===null?'timeout':'denied';
      guardDisabled=true; // A denied or unanswered service must not be spammed.
    }
    diagPersist();renderDiag();
  }
  function pingOverlayGuard(){
    if(!active||suspended||isHidden()||guardDisabled||guardPending)return;
    guardPending=true;guardState='waiting';
    guardDeadline=setTimeout(function(){finishOverlayGuard(null);},6500);
    var service='luna://com.webos.surfacemanager.screenSaver';
    try{
      if(window.webOS&&window.webOS.service&&typeof window.webOS.service.request==='function'){
        window.webOS.service.request(service,{
          method:'restartUserActivityTimer',parameters:{},
          onSuccess:function(r){finishOverlayGuard(r);},
          onFailure:function(r){finishOverlayGuard(r||{});}
        });
      }else if(typeof window.PalmServiceBridge==='function'){
        guardBridge=new window.PalmServiceBridge();
        guardBridge.onservicecallback=function(r){finishOverlayGuard(r);};
        guardBridge.call(service+'/restartUserActivityTimer','{}');
      }else{
        guardPending=false;
        clearTimeout(guardDeadline);guardDeadline=null;
        guardState='unavailable';guardDisabled=true;
        diagPersist();renderDiag();
      }
    }catch(e){finishOverlayGuard({returnValue:false});}
  }
  function diagRecord(kind){
    // Never write untrusted error strings, URLs or exception stacks.
    if(!/^(start|first-frame|hidden|visible|pagehide|beforeunload|manual-close|relaunch|js-error|promise-error)$/.test(kind))return;
    var t=Date.now(),age=diagSeconds(t,diagStart);
    var last=diagSession.events[diagSession.events.length-1];
    if(last && last[1]===kind && last[0]===age)return;
    diagSession.events.push([age,kind]);
    if(diagSession.events.length>12)diagSession.events.shift();
    diagSession.last=t;diagSession.lastEvent=kind;
    diagPersist();renderDiag();
  }
  function diagHeartbeat(){
    if(!active||suspended||isHidden())return;
    diagSession.last=Date.now();diagPersist();
    if(diagOpen)renderDiag();
  }
  function diagSummary(s){
    if(!s)return 'Данных предыдущего запуска нет';
    var events=[];
    for(var i=Math.max(0,s.events.length-10);i<s.events.length;i++){
      var entry=s.events[i];
      if(Array.isArray(entry)&&entry.length===2 &&
         typeof entry[0]==='number' && entry[0]>=0 && entry[0]<=86400 &&
         typeof entry[1]==='string' &&
         /^(start|first-frame|hidden|visible|pagehide|beforeunload|manual-close|relaunch|js-error|promise-error)$/.test(entry[1])){
        events.push(Math.floor(entry[0])+'с '+entry[1]);
      }
    }
    var endType=(s.lastEvent==='manual-close'||s.lastEvent==='beforeunload'||s.lastEvent==='pagehide')?
      'передано событие закрытия':s.lastEvent==='hidden'?
      'webOS сообщила о скрытии окна':
      'нет события закрытия (возможна остановка webOS)';
    return 'Длительность: '+diagSeconds(s.last,s.started)+' с\n'+
      'Последнее событие: '+(/^[a-z-]{1,24}$/.test(s.lastEvent)?s.lastEvent:'неизвестно')+'\n'+
      'Завершение: '+endType+'\n'+
      'Диагностика была открыта: '+(s.panelOpen?'да':'нет / не записано')+'\n'+
      'Режим потока: '+(s.frameMode==='mjpeg'?'MJPEG':s.frameMode==='snapshots'?
        'JPEG-снимки':s.frameMode==='jpeg-fallback'?'JPEG резервный':s.frameMode==='connecting'?'подключение':'не записан')+'\n'+
      'Возраст подтверждённого кадра¹: '+(typeof s.imageAge==='number'?
        s.imageAge+' с':'неизвестен')+'\n'+
      'Таймер overlay: '+guardText(s.guardState,s.guardCount)+'\n'+
      'Без действий с пультом: '+(typeof s.userIdle==='number'?
        s.userIdle+' с':'неизвестно')+'\n'+
      'События: '+(events.join(', ')||'не записаны');
  }
  function renderDiag(){
    if(!diagOpen||!diagText)return;
    var recentHistory='';
    for(var j=1;j<diagArchive.length&&j<4;j++){
      recentHistory+='\nЗапуск -'+j+': '+diagSeconds(diagArchive[j].last,diagArchive[j].started)+
        ' с · '+diagArchive[j].lastEvent+
        ' · окно диагностики '+(diagArchive[j].panelOpen?'открыто':'не отмечено');
    }
    var visibleImage=mjpegActive?mjpegImage:current;
    var imageWidth=Number(visibleImage&&visibleImage.naturalWidth)||0;
    var imageHeight=Number(visibleImage&&visibleImage.naturalHeight)||0;
    diagText.textContent='ТЕКУЩИЙ ЗАПУСК · '+diagSeconds(Date.now(),diagStart)+' с\n'+
      'Тип окна: стороннее webOS overlay, не системный LG Multi-View\n'+
      'Видимость: '+(isHidden()?'скрыто':suspended?'приостановлено':'видимо')+'\n'+
      'Режим: '+(mjpegActive?'непрерывный MJPEG':'JPEG-снимки / ожидание')+'\n'+
      'Качество: '+(quality==='max'?'максимальное':'обычное')+'\n'+
      'JPEG исходник: '+(lastFrameAt&&imageWidth&&imageHeight?imageWidth+'×'+imageHeight:'ожидаю кадр')+'\n'+
      'Последнее JS-событие кадра¹: '+(lastFrameAt?Math.floor((Date.now()-lastFrameAt)/1000)+' с назад':'нет')+'\n'+
      'Проверка источника MJPEG: '+(mjpegProducerLastOk?
        Math.floor((Date.now()-mjpegProducerLastOk)/1000)+' с назад · ошибок подряд '+mjpegProbeFailures:
        'ещё не подтверждена')+'\n'+
      'Таймер overlay: '+guardText(guardState,guardPingCount)+'\n'+
      'Последний ответ webOS: '+(guardLastOk?
        Math.floor((Date.now()-guardLastOk)/1000)+' с назад':'нет')+'\n'+
      'Без действий с пультом: '+diagSeconds(Date.now(),lastRemoteActivityAt)+' с\n\n'+
      'ПРЕДЫДУЩИЙ ЗАПУСК\n'+diagSummary(diagPrevious)+'\n\n'+
      'ИСТОРИЯ ПОСЛЕДНИХ ЗАПУСКОВ'+(recentHistory||'\nПока нет более ранних запусков')+'\n\n'+
      '¹ В MJPEG изображение может обновляться без JS-событий кадра.\n'+
      'Отсутствие события завершения не доказывает сбой: webOS может завершить процесс без уведомления.';
  }
  function toggleDiag(force){
    if(!diagPanel)return;
    diagOpen=typeof force==='boolean'?force:!diagOpen;
    diagPanel.hidden=!diagOpen;
    if(diagOpen){
      menuOpen=false;menu.hidden=true;
      ptzOpen=false;ptzPanel.hidden=true;ptzButton.setAttribute('aria-expanded','false');
      soundOpen=false;soundPanel.hidden=true;
      renderDiag();
      if(diagClose&&diagClose.focus)diagClose.focus();
    }else if(menuButton&&menuButton.focus)menuButton.focus();
    showControls();
  }
  diagPersist();
  function preference(key,def){try{return localStorage.getItem('go2rtc-pip-'+key)||def;}catch(e){return def;}}
  function save(key,value){try{localStorage.setItem('go2rtc-pip-'+key,value);}catch(e){}}
  function bounded(s,options,def){return options.indexOf(s)>=0?s:def;}
  function clear(t){if(t)clearTimeout(t);return null;}
  // LG webOS 4.x and older WebKit can expose only the vendor-prefixed API.
  function isHidden(){return !!(document.hidden||document.webkitHidden);}
  function setStatus(value) {
    var state=value==='live'?'live':value==='offline'?'offline':value==='setup'?'setup':'connecting';
    status.setAttribute('data-state',state);
    // Update only the label; keep the status container and its shared glass
    // styling in place. The colored dot represents the live connection.
    var statusLabel=document.getElementById('statusLabel');
    if(statusLabel)statusLabel.textContent=
      state==='live'?'LIVE':state==='offline'?'НЕТ':
      state==='setup'?'НАСТР':'···';
    var label=state==='live'?'Камера в эфире':
      state==='offline'?'Нет соединения с камерой':
      state==='setup'?'Настройка камеры':'Подключение к камере';
    status.setAttribute('aria-label',label);
    status.setAttribute('title',label);
  }
  function setSound(value) {
    var state=value==='active'||value==='quiet'?value:'unavailable';
    sound.setAttribute('data-state',state);
    sound.setAttribute('aria-label',state==='active'?'Обнаружен звук камеры':
      state==='quiet'?'Событий звука нет':'Звуковой монитор недоступен');
    // The always-visible microphone glyph is red only on detected events,
    // gray otherwise; it is NOT an indicator of audio-listening playback.
    if(soundInfo){
      soundInfo.textContent=state==='active'?'Обнаружен звук':
        state==='quiet'?'Тишина':'Монитор звука пока недоступен';
    }
  }
  function showControls() {
    pip.setAttribute('data-controls','visible');
    controlsTimer=clear(controlsTimer);
    controlsTimer=setTimeout(function(){
      if(!menuOpen&&!ptzOpen&&!soundOpen&&!diagOpen)pip.setAttribute('data-controls','subtle');
    },4500);
  }
  function notice(text) {
    toast.textContent=text;toast.hidden=false;
    toastTimer=clear(toastTimer);
    toastTimer=setTimeout(function(){toast.hidden=true;},2400);
  }
  function pixelWidth(){
    var w=full?1920:({small:352,medium:480,large:768}[size]||480);
    if(full)w=Math.max(w,1280);
    return Math.max(320,Math.min(Number(cfg.native_jpeg_width)||640,Math.round(w*1.05)));
  }
  // In old webOS builds CSS object-fit can leave black letterboxing.
  // Calculate a centre crop explicitly from the actual JPEG dimensions.
  function fitImage(img){
    if(!img||!img.style)return;
    var w=pip.clientWidth||(full?(window.innerWidth||1920):({small:352,medium:480,large:768}[size]||480));
    var h=pip.clientHeight||(full?(window.innerHeight||1080):({small:198,medium:270,large:432}[size]||270));
    var nw=Number(img.naturalWidth)||0,nh=Number(img.naturalHeight)||0;
    if(!nw||!nh||!w||!h)return;
    var ratio=fit==='cover'?Math.max(w/nw,h/nh):Math.min(w/nw,h/nh);
    var dw=Math.ceil(nw*ratio),dh=Math.ceil(nh*ratio);
    img.style.width=dw+'px';img.style.height=dh+'px';
    img.style.left=Math.floor((w-dw)/2)+'px';img.style.top=Math.floor((h-dh)/2)+'px';
    img.style.right='auto';img.style.bottom='auto';
  }
  function stopJPEG() {
    jpgTimer=clear(jpgTimer);jpgDeadline=clear(jpgDeadline);requestSeq++;
    frame.onload=frame.onerror=secondary.onload=secondary.onerror=null;
    frame.removeAttribute('src');secondary.removeAttribute('src');
  }
  function stopMJPEG() {
    mjpegDeadline=clear(mjpegDeadline);mjpegPoll=clear(mjpegPoll);mjpegRetryTimer=clear(mjpegRetryTimer);
    mjpegHealthDeadline=clear(mjpegHealthDeadline);
    if(mjpegHealthImage){mjpegHealthImage.onload=mjpegHealthImage.onerror=null;
      mjpegHealthImage.removeAttribute('src');mjpegHealthImage=null;}
    mjpegProbeFailures=0;mjpegLastProbe=0;
    mjpegGeneration++;
    mjpegImage.onload=mjpegImage.onerror=null;
    mjpegImage.removeAttribute('src');mjpegImage.hidden=true;
    mjpegPending=false;mjpegActive=false;
  }
  function currentMJPEGSource(){return quality==='max'&&maxMJPEGSources[chosen]?maxMJPEGSources[chosen]:mjpegSources[chosen]||'';}
  function currentFrameSource(){return currentMJPEGSource()||chosen;}
  function startMJPEG() {
    var source=directMJPEG?'direct':currentMJPEGSource();
    if(!active||suspended||privacy||!chosen||!source||mjpegPending||mjpegActive||Date.now()<mjpegRetryAt)return false;
    var attempt=++mjpegGeneration;
    mjpegPending=true;
    function failed(){
      if(attempt!==mjpegGeneration)return;
      stopMJPEG();
      mjpegFailures++;mjpegRetryAt=Date.now()+Math.min(12000,2500*mjpegFailures);
      setStatus('connecting');
      // Native multipart MJPEG is optional on older webOS engines.
      // On a decode error, keep the standard JPEG fallback alive.
      if(directMJPEG){
        setStatus('offline');
        mjpegRetryTimer=setTimeout(startMJPEG,Math.min(12000,2500*mjpegFailures));
      }else{
        scheduleJPEG(Math.max(600,pollMs));
      }
    }
    function received(){
      if(attempt!==mjpegGeneration||!active||suspended||privacy)return;
      // Some WebKit builds fire onload for subsequent multipart frames.
      if(mjpegActive){lastFrameAt=Date.now();return;}
      mjpegPending=false;mjpegActive=true;mjpegLastProbe=Date.now();mjpegFailures=0;mjpegRetryAt=0;
      mjpegDeadline=clear(mjpegDeadline);mjpegPoll=clear(mjpegPoll);
      stopJPEG();current.hidden=true;next.hidden=true;
      fitImage(mjpegImage);mjpegImage.hidden=false;
      lastFrameAt=Date.now();setStatus('live');
      if(!diagFirstFrame){diagFirstFrame=true;diagRecord('first-frame');}
    }
    // Some webOS WebKit builds do not fire onload for multipart MJPEG.
    // naturalWidth becomes available after the first decoded JPEG part.
    function probeFirstFrame(){
      if(attempt!==mjpegGeneration||!mjpegPending)return;
      if(mjpegImage.naturalWidth>0&&mjpegImage.naturalHeight>0){received();return;}
      mjpegPoll=setTimeout(probeFirstFrame,250);
    }
    mjpegImage.onload=received;mjpegImage.onerror=failed;
    mjpegImage.src=directMJPEG?directCameraURL:base+'/api/stream.mjpeg?src='+encodeURIComponent(source);
    mjpegDeadline=setTimeout(failed,12000);
    mjpegPoll=setTimeout(probeFirstFrame,250);
    return true;
  }
  function stopStream() {
    stopJPEG();stopMJPEG();lastFrameAt=0;successes=0;failures=0;
  }
  function failure() {
    if(!active||suspended||privacy||!chosen)return;
    failures++;
    if(failures>=2 && (!lastFrameAt||Date.now()-lastFrameAt>2200))setStatus('offline');
    if(mode==='jpeg'&&!mjpegPending&&!mjpegActive)startMJPEG();
    scheduleJPEG(Math.min(4500,500*Math.pow(2,Math.min(failures,3))));
  }
  function scheduleJPEG(delay) {
    if(!active||suspended||privacy||mjpegActive)return;
    jpgTimer=clear(jpgTimer);
    // A pending continuous stream is preferable to repeated H264 -> JPEG
    // FFmpeg processes. Keep the last visible JPEG while it opens.
    if(mjpegPending)delay=Math.max(1000,delay);
    jpgTimer=setTimeout(loadJPEG,delay);
  }
  function loadJPEG() {
    if(!active||suspended||privacy||!chosen||mjpegActive)return;
    var seq=++requestSeq, img=next;
    img.onload=function(){
      if(seq!==requestSeq||!active||suspended||privacy)return;
      jpgDeadline=clear(jpgDeadline);
      // Flip buffers only after decoding; never show a blank rectangle.
      fitImage(img);current.hidden=true;img.hidden=false;
      current=img;next=current===frame?secondary:frame;
      img.onload=img.onerror=null;failures=0;successes++;lastFrameAt=Date.now();
      setStatus('live');
      if(!diagFirstFrame){diagFirstFrame=true;diagRecord('first-frame');}
      scheduleJPEG(pollMs);
      // All normal PiP modes render JPEG images, never a hardware video
      // decoder. A persistent multipart stream avoids one FFmpeg per frame.
      if(mode==='jpeg' && !mjpegPending && !mjpegActive){startMJPEG();}
    };
    img.onerror=function(){if(seq!==requestSeq)return;jpgDeadline=clear(jpgDeadline);failure();};
    var source=currentFrameSource();
    img.src=directCameraURL?directCameraURL+'?_='+Date.now()+'_'+seq:
      base+'/api/frame.jpeg?src='+encodeURIComponent(source)+'&w='+pixelWidth()+'&_='+Date.now()+'_'+seq;
    jpgDeadline=setTimeout(function(){
      if(seq!==requestSeq)return;
      requestSeq++;img.onload=img.onerror=null;img.removeAttribute('src');
      failure();
    },timeoutMs);
  }
  function restartStream() {
    stopStream();
    if(!active||suspended||privacy||!chosen)return;
    current=frame;next=secondary;frame.hidden=false;secondary.hidden=true;
    setStatus('connecting');
    // Keep an independent JPEG fallback while the multipart endpoint warms.
    if(directMJPEG)startMJPEG();
    else {loadJPEG();if(mode==='jpeg')startMJPEG();}
  }
  // Camera source list is taken only from go2rtc. No packaged camera inventory.
  function normalizeCatalog(found) {
    var list=Array.isArray(found.cameras)?found.cameras:null;
    var names=list?list.map(function(c){return c && c.stream;}):Object.keys(found);
    var seen={},out=[];
    names.forEach(function(n){
      if(typeof n!=='string'||!n||n.length>=100||
         /[\\/?#@\x00-\x1f]/.test(n)||
         /(_pip_mjpeg|_pip_max_mjpeg|_tv_aac)$/.test(n)||seen[n])return;
      seen[n]=true;
      if(out.length<64)out.push(n);
    });
    return out.sort();
  }
  function resolveJPEGStreams(found,names) {
    var mapped={},present={},list=Array.isArray(found.cameras)?found.cameras:null;
    if(list){
      list.forEach(function(c){
        if(!c||typeof c.stream!=='string'||typeof c.mjpeg_stream!=='string')return;
        if(names.indexOf(c.stream)>=0&&c.mjpeg_stream.indexOf(c.stream+'_')===0 &&
           !/[\\/?#@\x00-\x1f]/.test(c.mjpeg_stream))mapped[c.stream]=c.mjpeg_stream;
      });
    }else{
      Object.keys(found).forEach(function(name){present[name]=true;});
      names.forEach(function(name){
        var technical=name+'_pip_mjpeg';
        if(present[technical])mapped[name]=technical;
      });
    }
    return mapped;
  }
  function resolveMaxMJPEGStreams(found,names) {
    var mapped={},present={},list=Array.isArray(found.cameras)?found.cameras:null;
    if(list){
      list.forEach(function(c){
        if(!c||typeof c.stream!=='string'||names.indexOf(c.stream)<0)return;
        var wanted=c.stream+'_pip_max_mjpeg';
        if(c.mjpeg_max_stream===wanted&&wanted.length<100)mapped[c.stream]=wanted;
      });
    }else{
      Object.keys(found).forEach(function(name){present[name]=true;});
      names.forEach(function(name){
        var wanted=name+'_pip_max_mjpeg';
        if(wanted.length<100&&present[wanted])mapped[name]=wanted;
      });
    }
    return mapped;
  }
  function discover() {
    if(!active||suspended||!base||directCameraURL)return;
    if(catalogRequest){try{catalogRequest.abort();}catch(e){}}
    var x=new XMLHttpRequest(),done=false;
    catalogRequest=x;
    function finish(valid,found,httpStatus) {
      if(done)return;done=true;clearTimeout(deadline);
      if(catalogRequest===x)catalogRequest=null;
      if(!active||suspended)return; // A hidden webOS app must not poll or restart streams.
      if(valid) {
        var names=normalizeCatalog(found),previous=chosen,previousSource=currentFrameSource();
        mjpegSources=resolveJPEGStreams(found,names);
        maxMJPEGSources=resolveMaxMJPEGStreams(found,names);
        var nativeCatalog=Array.isArray(found.cameras);
        ptzServer=nativeCatalog && found.ptz_enabled===true;
        ptzCamera=ptzServer && typeof found.ptz_camera==='string'?found.ptz_camera:'';
        soundMonitorAvailable=nativeCatalog && found.sound_enabled===true;
        cameras=names;
        if(!chosen||names.indexOf(chosen)<0)chosen=names[0]||'';
        ready=!!chosen;empty.hidden=ready;
        if(chosen)save('camera',chosen);
        if(!ready){empty.textContent='В go2rtc нет доступных камер';stopStream();setStatus('offline');}
        else if(chosen!==previous||currentFrameSource()!==previousSource){restartStream();soundTimer=clear(soundTimer);pollSound();}
        // Audio may exist on stock go2rtc even when optional PTZ/activity
        // endpoints are missing. Only accept a stream present in this catalog
        // and belonging to the selected physical camera.
        var wanted=chosen+'_tv_aac',available=false;
        if(nativeCatalog){
          available=found.audio_stream===wanted;
          if(!available)found.cameras.forEach(function(info){
            if(info&&info.stream===chosen&&info.audio_stream===wanted)available=true;
          });
        }else{
          available=Object.prototype.hasOwnProperty.call(found,wanted);
        }
        audioStream=available?wanted:'';
        if(audioStream)save('audio-stream',audioStream);
        else save('audio-stream','');
        if(soundOpen)warmAudio();
        soundUrl=soundMonitorAvailable?String(cfg.sound_url||base+'/api/webos-pip/activity'):'';
        soundTimer=clear(soundTimer);
        if(soundMonitorAvailable)pollSound();
        else setSound('unavailable');
      } else if(httpStatus===404 && api===base+'/api/webos-pip') {
        ptzServer=false;ptzCamera='';soundMonitorAvailable=false;setSound('unavailable');
        api=base+'/api/streams';
        // Upstream without the optional webOS module must not add five
        // seconds to first camera startup.
        discoveryTimer=setTimeout(discover,0);
        return;
      } else if(!chosen) {
        // file:// apps may be denied XMLHttpRequest by CORS. A missing
        // catalog must not leave the user looking at LOADING forever.
        empty.hidden=false;
        empty.textContent='Не удалось получить список камер';
        setStatus('offline');
      }
      discoveryTimer=setTimeout(discover,valid?catalogMs:5000);
    }
    var deadline=setTimeout(function(){try{x.abort();}catch(e){}finish(false,null,0);},4500);
    x.onreadystatechange=function(){
      if(x.readyState!==4)return;
      if(x.status===200){
        try{
          var data=JSON.parse(x.responseText);
          if(data&&typeof data==='object'&&!Array.isArray(data))return finish(true,data,200);
        }catch(e){}
      }
      finish(false,null,x.status);
    };
    try{x.open('GET',api,true);x.send();}catch(e){finish(false,null,0);}
  }
  function pollSound() {
    if(!active||suspended||privacy||!chosen||!soundMonitorAvailable||!soundUrl){setSound('unavailable');return;}
    if(monitorRequest){try{monitorRequest.abort();}catch(e){}}
    var x=new XMLHttpRequest(),finished=false;monitorRequest=x;
    function done(value) {
      if(finished)return;finished=true;clearTimeout(deadline);
      if(monitorRequest===x)monitorRequest=null;
      if(!active||suspended||privacy)return;
      setSound(value);
      soundTimer=setTimeout(pollSound,value==='unavailable'?4000:1000);
    }
    var deadline=setTimeout(function(){try{x.abort();}catch(e){}done('unavailable');},3000);
    x.onreadystatechange=function(){
      if(x.readyState!==4)return;
      if(x.status===200){
        try{return done(JSON.parse(x.responseText).state);}catch(e){}
      }
      done('unavailable');
    };
    try{x.open('GET',soundUrl,true);x.send();}catch(e){done('unavailable');}
  }
  function ptzReady() {
    return !!(ptzToken&&ptzServer&&base&&chosen&&(!ptzCamera||ptzCamera===chosen));
  }
  function ptzFocus(direction) {
    if(!ptzGrid)return;
    for(var i=0;i<ptzGrid.children.length;i++){
      var b=ptzGrid.children[i];
      if(b.getAttribute&&b.getAttribute('data-dir')===direction&&b.focus){
        b.focus();return;
      }
    }
  }
  function ptzToggle(force) {
    ptzOpen=typeof force==='boolean'?force:!ptzOpen;
    ptzEnterHeld=false;
    ptzPanel.hidden=!ptzOpen;
    ptzButton.setAttribute('aria-expanded',ptzOpen?'true':'false');
    if(ptzOpen){
      soundOpen=false;soundPanel.hidden=true;
      menuOpen=false;menu.hidden=true;
      // No token means visibly unavailable, never silently moves a camera.
      for(var i=0;i<ptzGrid.children.length;i++){
        var child=ptzGrid.children[i];
        if(child.tagName==='BUTTON')child.disabled=!ptzReady();
      }
      if(!ptzServer)notice('PTZ: модуль управления на сервере недоступен');
      else if(!ptzToken)notice('PTZ: требуется авторизация телевизора');
      else if(ptzCamera&&ptzCamera!==chosen)notice('PTZ: выбрана другая камера');
      if(ptzHint)ptzHint.textContent=!ptzServer?'Сервер не поддерживает PTZ':
        !ptzToken?'PTZ недоступен: ключ телевизора не передан':
        ptzCamera&&ptzCamera!==chosen?'PTZ: выбрана другая камера':'OK — шаг; Back — закрыть';
      ptzFocus('stop');
    }else if(ptzButton.focus)ptzButton.focus();
    showControls();
  }
  function ptzEmergencyStop() {
    // STOP is never blocked by an in-flight move. The server serializes
    // camera commands and authenticates STOP with the same scoped token.
    if(Date.now()-ptzLastStopAt<700)return;
    ptzLastStopAt=Date.now();
    var stop=new XMLHttpRequest();
    try {
      stop.open('POST',base+'/api/webos-pip/ptz',true);
      stop.setRequestHeader('Content-Type','application/json');
      stop.setRequestHeader('X-Go2RTC-PTZ',ptzToken);
      stop.send(JSON.stringify({direction:'stop'}));
      if(ptzHint)ptzHint.textContent='PTZ: команда Стоп отправлена';
    } catch(e) {if(ptzHint)ptzHint.textContent='PTZ: Стоп не доставлен';}
  }
  function ptzMove(direction) {
    if(!active||!ptzReady()||privacy)return;
    if(['up','down','left','right','stop'].indexOf(direction)<0)return;
    // Never enqueue motor moves while ONVIF is outstanding; however,
    // the emergency Stop control stays available.
    if(ptzBusy){
      if(direction==='stop'){ptzEmergencyStop();return;}
      if(ptzHint)ptzHint.textContent='PTZ: предыдущая команда ещё выполняется';
      return;
    }
    if(direction!=='stop'&&Date.now()-ptzLastMoveAt<1100){
      if(ptzHint)ptzHint.textContent='PTZ: один шаг на нажатие (пауза 1 с)';
      return;
    }
    if(direction!=='stop')ptzLastMoveAt=Date.now();
    ptzBusy=true;
    var x=new XMLHttpRequest(),finished=false,timeout=null,slowTimer=null;
    var startedAt=Date.now();
    ptzPanel.setAttribute('aria-busy','true');
    if(ptzHint)ptzHint.textContent='PTZ: поворачиваю камеру…';
    slowTimer=setTimeout(function(){
      if(!finished&&ptzHint)ptzHint.textContent='PTZ: ожидаю ONVIF-ответ камеры…';
    },1200);
    function done(status) {
      if(finished)return;
      finished=true;
      clearTimeout(timeout);clearTimeout(slowTimer);
      ptzBusy=false;
      ptzPanel.setAttribute('aria-busy','false');
      if(status===200){
        var duration=Math.max(0,Date.now()-startedAt);
        // Report actual observable round-trip; the server also writes
        // sanitized ONVIF timing details to the companion service journal.
        if(ptzHint)ptzHint.textContent='PTZ: готово за '+(duration/1000).toFixed(1)+' с';
        return;
      }
      var message=status===401?'PTZ: ключ авторизации отклонён':
        status===403?'PTZ: запрещён доступ из webOS':
        status===429?'PTZ: повторите через секунду':
        status===502?'PTZ: ONVIF отклонил команду движения':
        status===0?'PTZ: сеть или тайм-аут (26 с)':
        'PTZ: HTTP '+status;
      if(ptzHint)ptzHint.textContent=message;
      notice(message);
    }
    try {
      x.open('POST',base+'/api/webos-pip/ptz',true);
      x.setRequestHeader('Content-Type','application/json');
      x.setRequestHeader('X-Go2RTC-PTZ',ptzToken);
      x.onreadystatechange=function(){
        if(x.readyState===4)done(x.status||0);
      };
      x.onerror=function(){done(0);};
      // Leave a bounded deadline for capabilities lookup + move + safe Stop.
      timeout=setTimeout(function(){try{x.abort();}catch(e){}done(0);},26000);
      x.send(JSON.stringify({direction:direction}));
    }catch(e){done(0);}
  }
  function bindPTZ() {
    if(!ptzButton||!ptzPanel||!ptzGrid)return;
    ptzButton.onclick=function(){ptzToggle();};
    for(var i=0;i<ptzGrid.children.length;i++){
      var b=ptzGrid.children[i],dir=b.getAttribute&&b.getAttribute('data-dir');
      if(dir)(function(node,way){
        node.onclick=function(){ptzMove(way);showControls();};
      })(b,dir);
    }
  }
  function stopAudio() {
    audioRequestId++;
    audioTimer=clear(audioTimer);audioPlaying=false;audioLoading=false;
    if(cameraAudio) {
      cameraAudio.onplaying=null;cameraAudio.onerror=null;
      try{cameraAudio.pause();}catch(e){}
      cameraAudio.removeAttribute('src');
      try{cameraAudio.load();}catch(e){}
    }
    if(soundListen)soundListen.textContent='Слушать камеру';
    if(audioInfo)audioInfo.textContent='Прослушивание выключено';
  }
  function warmAudio() {
    // Prebuffer only after the user opens the sound panel. This does not
    // play camera audio or take over TV speaker focus.
    if(!base||!audioStream||!chosen||audioStream!==chosen+'_tv_aac'||
       !cameraAudio||audioPlaying||audioLoading)return;
    var url=base+'/api/stream.aac?src='+encodeURIComponent(audioStream);
    if(cameraAudio.src!==url){
      cameraAudio.preload='auto';
      cameraAudio.src=url;
      try{cameraAudio.load();}catch(e){}
    }
  }
  function toggleAudio() {
    if(audioPlaying||audioLoading){stopAudio();notice('Прослушивание выключено');return;}
    if(!base||!audioStream||!chosen||audioStream!==chosen+'_tv_aac'){
      if(audioInfo)audioInfo.textContent='AAC-поток отсутствует: проверь адрес go2rtc и камеру';
      notice('AAC-поток этой камеры не настроен');return;
    }
    if(!cameraAudio||!cameraAudio.play){
      if(audioInfo)audioInfo.textContent='TV: аудиовоспроизведение не поддерживается';
      notice('Звук не поддерживается телевизором');return;
    }
    var id=++audioRequestId;
    audioLoading=true;
    soundListen.textContent='Подключение звука…';
    if(audioInfo)audioInfo.textContent='Подключение к AAC…';
    function fail(message){
      if(id!==audioRequestId)return;
      stopAudio();
      if(audioInfo)audioInfo.textContent=message;
      notice(message);
    }
    cameraAudio.onplaying=function(){
      if(id!==audioRequestId)return;
      audioTimer=clear(audioTimer);audioLoading=false;audioPlaying=true;
      soundListen.textContent='Отключить прослушивание';
      if(audioInfo)audioInfo.textContent='AAC воспроизводится';
    };
    cameraAudio.onerror=function(){
      var code=cameraAudio.error&&Number(cameraAudio.error.code)||0;
      fail('AAC: ошибка плеера '+code+' (1=отмена, 2=сеть, 3=декодер, 4=формат)');
    };
    // Reuse the already buffered stream when the audio panel was opened.
    if(cameraAudio.src!==base+'/api/stream.aac?src='+encodeURIComponent(audioStream)){
      cameraAudio.src=base+'/api/stream.aac?src='+encodeURIComponent(audioStream);
    }
    audioTimer=setTimeout(function(){fail('AAC: нет воспроизведения в течение 15 секунд');},15000);
    try{
      var result=cameraAudio.play();
      if(result&&result.catch)result.catch(function(err){
        var code=err&&typeof err.name==='string'&&/^[A-Za-z]+Error$/.test(err.name)?err.name:'unknown';
        fail('AAC: TV play() отклонён ('+code+')');
      });
    }catch(e){fail('ТВ отказал в воспроизведении AAC');}
  }
  function soundToggle(force) {
    soundOpen=typeof force==='boolean'?force:!soundOpen;
    soundPanel.hidden=!soundOpen;
    if(soundOpen){
      warmAudio();
      ptzOpen=false;ptzPanel.hidden=true;
      ptzButton.setAttribute('aria-expanded','false');
      menuOpen=false;menu.hidden=true;
      if(soundMonitor.focus)soundMonitor.focus();
    }else if(sound.focus)sound.focus();
    showControls();
  }
  function bindSound() {
    if(!sound)return;
    sound.onclick=function(){soundToggle();};
    if(soundMonitor)soundMonitor.onclick=function(){soundToggle(false);};
    if(soundListen)soundListen.onclick=toggleAudio;
  }
  function applyWindow() {
    pip.setAttribute('data-size',full?'fullscreen':size);
    pip.setAttribute('data-corner',corner);
    pip.setAttribute('data-fit',fit);
    fitImage(current);fitImage(mjpegImage);
    // Start a new frame at the actual new size; never stretch a 352px JPEG
    // across a 1920px window and call that fullscreen.
    if(chosen&&!privacy){restartStream();}
  }
  function rebuildMenu() {
    while(menuItems.firstChild)menuItems.removeChild(menuItems.firstChild);
    var entries=[
      ['Размер: '+({small:'маленький',medium:'средний',large:'большой'}[size]),function(){size=sizes[(sizes.indexOf(size)+1)%sizes.length];save('size',size);full=false;applyWindow();}],
      ['Положение окна',function(){corner=corners[(corners.indexOf(corner)+1)%corners.length];save('corner',corner);applyWindow();}],
      [full?'Свернуть окно':'На весь экран',function(){full=!full;applyWindow();}],
      ['Кадр: '+(fit==='cover'?'заполнить':'без обрезки'),function(){fit=fit==='cover'?'contain':'cover';save('fit',fit);applyWindow();}],
      ['JPEG: '+(mode==='jpeg'?'поток кадров':'быстрые снимки'),function(){mode=mode==='jpeg'?'snapshots':'jpeg';save('mode',mode);restartStream();}],
      [privacy?'Показать камеру':'Скрыть камеру',function(){togglePrivacy();}],
      ['Диагностика окна',function(){toggleDiag(true);}]
    ];
    // Device owners can switch between go2rtc and direct HTTP cameras.
    // A source provisioned by an external launcher or packaged config is
    // owned by that provisioner and must not appear mutable in the TV UI.
    if(!cfg.url&&!launchServer)entries.push(['Сменить источник',function(){
      save('server','');save('camera','');save('mjpeg','');
      // Do not reuse a previous server's scoped PTZ token or audio alias.
      save('ptz-token','');save('audio-stream','');
      if(window.location&&window.location.reload)window.location.reload();
    }]);
    // Only offer true high quality when a separately configured source is verified.
    if(maxMJPEGSources[chosen])entries.splice(5,0,
      ['Качество: '+(quality==='max'?'максимальное':'обычное'),function(){quality=quality==='max'?'standard':'max';save('quality',quality);restartStream();}]);
    if(cameras.length>1)entries.unshift(['Камера: '+(chosen||'—'),function(){
      var index=cameras.indexOf(chosen);chosen=cameras[(index+1)%cameras.length];
      save('camera',chosen);restartStream();pollSound();
    }]);
    entries.forEach(function(e){
      var b=document.createElement('button');b.className='menu-item';b.textContent=e[0];
      b.onclick=function(){e[1]();rebuildMenu();showControls();};
      menuItems.appendChild(b);
    });
  }
  function menuToggle(force) {
    menuOpen=typeof force==='boolean'?force:!menuOpen;
    if(menuOpen){
      if(diagOpen){diagOpen=false;diagPanel.hidden=true;}
      ptzOpen=false;ptzPanel.hidden=true;ptzButton.setAttribute('aria-expanded','false');
      soundOpen=false;soundPanel.hidden=true;
    }
    menu.hidden=!menuOpen;showControls();
    if(menuOpen){rebuildMenu();var first=menuItems.firstChild;if(first&&first.focus)first.focus();}
    else if(menuButton.focus)menuButton.focus();
  }
  function togglePrivacy() {
    privacy=!privacy;save('privacy',privacy?'true':'false');pip.setAttribute('data-private',privacy?'true':'false');
    if(privacy){stopStream();stopAudio();soundTimer=clear(soundTimer);if(monitorRequest)monitorRequest.abort();setSound('unavailable');}
    else {restartStream();pollSound();}
    notice(privacy?'Камера скрыта':'Камера включена');
  }
  function close() {
    diagRecord('manual-close');
    active=false;stopStream();stopAudio();soundTimer=clear(soundTimer);discoveryTimer=clear(discoveryTimer);
    controlsTimer=clear(controlsTimer);toastTimer=clear(toastTimer);watchdog=clear(watchdog);
    if(catalogRequest)catalogRequest.abort();if(monitorRequest)monitorRequest.abort();
    window.close();
  }
  function handleKey(e) {
    var k=e.keyCode;
    showControls();
    if(k===461||k===27||k===8){
      if(e.preventDefault)e.preventDefault();
      if(diagOpen)toggleDiag(false);
      else if(ptzOpen)ptzToggle(false);
      else if(soundOpen)soundToggle(false);
      else if(menuOpen)menuToggle(false);
      return; // LG Back should never accidentally close the overlay.
    }
    if(k===403){if(e.preventDefault)e.preventDefault();togglePrivacy();return;}
    if(k===404){if(e.preventDefault)e.preventDefault();size=sizes[(sizes.indexOf(size)+1)%sizes.length];full=false;save('size',size);applyWindow();return;}
    if(k===405){if(e.preventDefault)e.preventDefault();full=!full;applyWindow();return;}
    if(k===406){if(e.preventDefault)e.preventDefault();menuToggle();return;}
    if(ptzOpen && (k===38||k===40||k===37||k===39)){
      // Arrow keys move remote focus; only OK/Enter triggers a motor step.
      ptzFocus(k===38?'up':k===40?'down':k===37?'left':'right');
      if(e.preventDefault)e.preventDefault();
      return;
    }
    if(k===38||k===40) {
      var focusables=[];
      if(menuOpen){
        for(var i=0;i<menuItems.children.length;i++)focusables.push(menuItems.children[i]);
      } else if(!base) {
        focusables=[document.getElementById('serverAddress'),document.getElementById('serverSave'),menuButton,closeButton];
      }
      if(focusables.length) {
        var currentFocus=e.target||document.activeElement,position=focusables.indexOf(currentFocus);
        var nextFocus=focusables[(position+(k===40?1:focusables.length-1))%focusables.length];
        if(nextFocus&&nextFocus.focus)nextFocus.focus();
        if(e.preventDefault)e.preventDefault();
        return;
      }
    }
    if(k===13){
      var target=e.target||document.activeElement;
      var ptzDirection=ptzOpen&&target&&target.getAttribute&&target.getAttribute('data-dir');
      if(ptzDirection){
        if(e.preventDefault)e.preventDefault();
        if(e.repeat||ptzEnterHeld)return;
        ptzEnterHeld=true;
        if(target.click)target.click();else if(target.onclick)target.onclick();
        return;
      }
      if(target&&target.tagName==='BUTTON') {
        if(e.preventDefault)e.preventDefault();
        if(target.click)target.click();else if(target.onclick)target.onclick();
        return;
      }
      if(target&&target.tagName==='INPUT')return;
      if(!menuOpen){if(e.preventDefault)e.preventDefault();menuToggle(true);}
    }
  }
  function handleKeyUp(e) {
    if(e.keyCode===13)ptzEnterHeld=false;
  }
  function suspend() {
    if(!active||suspended)return;
    suspended=true;stopStream();stopAudio();
    soundTimer=clear(soundTimer);discoveryTimer=clear(discoveryTimer);
    if(catalogRequest){try{catalogRequest.abort();}catch(e){}catalogRequest=null;}
    if(monitorRequest){try{monitorRequest.abort();}catch(e){}monitorRequest=null;}
    setSound('unavailable');
  }
  function resume(force) {
    if(!active||isHidden()||(!suspended&&!force))return;
    suspended=false;
    setStatus('connecting');restartStream();
    discoveryTimer=clear(discoveryTimer);discover();
    soundTimer=clear(soundTimer);pollSound();
  }
  function applyLaunchServer(event) {
    var incoming=serverFromLaunch(event),preferred=cameraFromLaunch(event);
    if(incoming)save('server',incoming);
    var newToken=ptzTokenFromLaunch(event),newAudio=audioStreamFromLaunch(event);
    if(newToken){ptzToken=newToken;save('ptz-token',newToken);}
    if(newAudio){audioStream=newAudio;save('audio-stream',newAudio);}
    if(preferred.camera){
      save('camera',preferred.camera);
      if(preferred.mjpeg)save('mjpeg',preferred.mjpeg);
    }
    if((incoming&&incoming!==base) ||
       (preferred.camera&&preferred.camera!==chosen)) {
      // Save before reloading: the launcher may not repeat params later.
      if(window.location&&window.location.reload)window.location.reload();
      return true;
    }
    return false;
  }
  function handleRelaunch(event) {
    diagRecord('relaunch');
    if(applyLaunchServer(event))return;
    try{
      if(window.webOSSystem && window.webOSSystem.activate)window.webOSSystem.activate();
      else if(window.PalmSystem && window.PalmSystem.activate)window.PalmSystem.activate();
    }catch(e){}
    resume(true);
  }
  function probeMJPEGHealth() {
    // A multipart <img> does not reliably emit onload per frame on old LG WebKit.
    // A single bounded JPEG snapshot verifies that its upstream producer is alive.
    // Never add a second permanent decoder or poll faster than every 20s.
    var source=currentMJPEGSource();
    if(!source||!mjpegActive||mjpegHealthImage)return;
    var probe=document.createElement('img'),finished=false;
    mjpegHealthImage=probe;mjpegLastProbe=Date.now();
    function finish(ok){
      if(finished||mjpegHealthImage!==probe)return;
      finished=true;mjpegHealthDeadline=clear(mjpegHealthDeadline);
      probe.onload=probe.onerror=null;probe.removeAttribute('src');mjpegHealthImage=null;
      if(!active||suspended||privacy||!mjpegActive)return;
      if(ok){mjpegProbeFailures=0;mjpegProducerLastOk=Date.now();return;}
      mjpegProbeFailures++;
      if(mjpegProbeFailures>=2){
        stopMJPEG();mjpegRetryAt=Date.now()+2500;
        setStatus('connecting');loadJPEG();
      }
    }
    probe.onload=function(){finish(true);};probe.onerror=function(){finish(false);};
    mjpegHealthDeadline=setTimeout(function(){finish(false);},7500);
    probe.src=base+'/api/frame.jpeg?src='+encodeURIComponent(source)+'&_health='+mjpegLastProbe;
  }
  function health() {
    if(!active||suspended||privacy||!chosen)return;
    if(mjpegActive){
      if(!directMJPEG&&!mjpegHealthImage&&Date.now()-mjpegLastProbe>=20000)probeMJPEGHealth();
      return;
    }
    if(lastFrameAt && Date.now()-lastFrameAt>Math.max(6000,timeoutMs+1500))setStatus('offline');
  }
  function init() {
    var setup=document.getElementById('setup'), address=document.getElementById('serverAddress');
    var saveServer=document.getElementById('serverSave');
    if(setup)setup.hidden=!!base;
    if(address)address.value=base;
    if(saveServer)saveServer.onclick=function(){
      var value=String(address.value||'').trim().replace(/\/+$/,'');
      if(!validDirectCameraURL(value) && (!/^https?:\/\/[a-z0-9.\-\[\]:]+(?::[0-9]{2,5})?$/i.test(value) ||
         /[@/?#]/.test(value.substring(value.indexOf('://')+3)))){
        notice('Укажи адрес go2rtc или локальный HTTP JPEG/MJPEG');return;
      }
      save('server',value);window.location.reload();
    };
    // UI and remote handlers must work even before any server is configured.
    // Previously this early return left ... / Close entirely inoperative.
    menuButton.onclick=function(){menuToggle();};
    closeButton.onclick=close;
    if(diagPanel)diagPanel.hidden=true;
    if(diagClose)diagClose.onclick=function(){toggleDiag(false);};
    bindPTZ();bindSound();
    document.addEventListener('keydown',handleKey,false);
    document.addEventListener('keyup',handleKeyUp,false);
    document.addEventListener('mousemove',showControls,false);
    // Only real input is measured. The service call below is not reported as
    // user input, and never invokes a remote-control key handler.
    window.addEventListener('keydown',function(){lastRemoteActivityAt=Date.now();},true);
    window.addEventListener('mousemove',function(){lastRemoteActivityAt=Date.now();},true);
    window.addEventListener('click',function(){lastRemoteActivityAt=Date.now();},true);
    // First attempt quickly exposes permission failures; subsequent calls are
    // at most once per 45 seconds and only while our overlay is visible.
    setTimeout(pingOverlayGuard,2500);
    setInterval(pingOverlayGuard,45000);
    // Event listeners must also run when there is no server saved yet.
    // On fresh webOS launch, params may arrive only after DOMContentLoaded.
    document.addEventListener('webOSLaunch',function(e){diagRecord('relaunch');applyLaunchServer(e);},true);
    document.addEventListener('webOSRelaunch',handleRelaunch,false);
    window.addEventListener('webOSRelaunch',handleRelaunch);
    if(!base) {
      empty.textContent='Адрес go2rtc или камеры пока не настроен';
      setStatus('setup');
      if(address&&address.focus)address.focus();
      return;
    }
    save('server',base);
    size=bounded(preference('size','medium'),sizes,'medium');
    corner=bounded(preference('corner','tr'),corners,'tr');
    fit=bounded(preference('fit','cover'),['cover','contain'],'cover');
    // Preserve other preferences, but migrate legacy contain letterboxing once.
    if(preference('fit_layout_v1','')!=='cover-default'){
      fit='cover';save('fit','cover');save('fit_layout_v1','cover-default');
    }
    // Migrate former 'auto' preferences to JPEG: never request an MSE decoder.
    mode=bounded(preference('mode','jpeg'),['jpeg','snapshots'],'jpeg');
    quality=bounded(preference('quality','standard'),['standard','max'],'standard');
    chosen=directCameraURL?'direct-camera':preference('camera','');
    var verifiedMJPEG=preference('mjpeg','');
    if(chosen&&verifiedMJPEG===chosen+'_pip_mjpeg'){
      mjpegSources[chosen]=verifiedMJPEG;
    }
    privacy=preference('privacy','false')==='true';
    pip.setAttribute('data-private',privacy?'true':'false');
    pip.setAttribute('data-size',size);pip.setAttribute('data-corner',corner);
    pip.setAttribute('data-fit',fit);
    setStatus('connecting');setSound('unavailable');
    function onVisibility(){
      if(isHidden()){diagRecord('hidden');suspend();}
      else{diagRecord('visible');resume(false);}
    }
    document.addEventListener('visibilitychange',onVisibility,false);
    document.addEventListener('webkitvisibilitychange',onVisibility,false);
    // Both webOSRelaunch listeners were bound above before the early setup
    // return; do not bind a duplicate event and start a second stream.
    window.addEventListener('pagehide',function(){diagRecord('pagehide');suspend();});
    window.addEventListener('beforeunload',function(){diagRecord('beforeunload');active=false;suspend();stopStream();});
    window.addEventListener('error',function(){diagRecord('js-error');});
    window.addEventListener('unhandledrejection',function(){diagRecord('promise-error');});
    setInterval(health,1000);
    setInterval(diagHeartbeat,5000);
    if(isHidden()){
      diagRecord('hidden');
      suspended=true;
      empty.hidden=false;
      empty.textContent='PiP приостановлено webOS (невидимое окно)';
      setStatus('offline');
      return;
    }
    // The paired MCPshka launcher supplies a verified physical camera and
    // JPEG stream. <img> can fetch it without cross-origin XHR or CORS.
    // Catalog discovery remains optional for selection of more cameras.
    if(chosen){
      cameras=[chosen];ready=true;empty.hidden=true;restartStream();
    }
    discover();
    pollSound();
  }
  init();
})();