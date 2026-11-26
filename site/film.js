(function(){
'use strict';
var R=document.documentElement;
var reduce=matchMedia('(prefers-reduced-motion: reduce)').matches;
var mobile=matchMedia('(max-width:640px)').matches;
var pageVisible=!document.hidden;
document.addEventListener('visibilitychange',function(){pageVisible=!document.hidden;});

/* ---------- basics (work with or without motion) ---------- */
var tb=document.getElementById('theme');
if(tb)tb.addEventListener('click',function(){
  var cur=R.getAttribute('data-theme');
  if(!cur)cur=matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light';
  var next=cur==='dark'?'light':'dark';
  R.setAttribute('data-theme',next);
  try{localStorage.setItem('aits-theme',next);}catch(e){}
});
function cmds(pre){return pre.textContent.split('\n').filter(function(l){return l.indexOf('$ ')===0;}).map(function(l){return l.slice(2).replace(/\s+#.*$/,'');}).join('\n');}
document.querySelectorAll('.panel .turn').forEach(function(t){
  var pre=t.querySelector('pre'),who=t.querySelector('.who');
  if(!pre||!who||!/^(you|stdin|text)$/i.test(who.textContent.trim()))return;
  var txt=cmds(pre);if(!txt)return;
  var b=document.createElement('button');b.type='button';b.className='copy cpl';b.textContent='Copy';
  b.setAttribute('data-copy',txt);b.setAttribute('aria-label','Copy command');t.appendChild(b);
});
document.querySelectorAll('.copy').forEach(function(b){
  b.addEventListener('click',function(){
    var txt=b.getAttribute('data-copy');
    function ok(){b.textContent='Copied';b.classList.add('done');setTimeout(function(){b.textContent='Copy';b.classList.remove('done');},1600);}
    function fb(){var ta=document.createElement('textarea');ta.value=txt;ta.style.position='fixed';ta.style.left='-9999px';document.body.appendChild(ta);ta.select();try{document.execCommand('copy');ok();}catch(e){}document.body.removeChild(ta);}
    if(navigator.clipboard&&navigator.clipboard.writeText)navigator.clipboard.writeText(txt).then(ok,fb);else fb();
  });
});
document.querySelectorAll('.json pre').forEach(function(pre){
  var w=document.createTreeWalker(pre,NodeFilter.SHOW_TEXT),n,l=[];while((n=w.nextNode()))l.push(n);
  l.forEach(function(t){if(t.nodeValue.indexOf(',')>-1)t.nodeValue=t.nodeValue.replace(/,(?!\s)/g,',​');});
});
document.querySelectorAll('[role="tablist"]').forEach(function(list){
  var tabs=[].slice.call(list.querySelectorAll('[role="tab"]'));
  function sel(t,focus){
    tabs.forEach(function(x){
      var s=x===t,p=document.getElementById(x.getAttribute('aria-controls'));
      x.setAttribute('aria-selected',s);x.tabIndex=s?0:-1;p.classList.toggle('off',!s);
      if(s)p.removeAttribute('aria-hidden');else p.setAttribute('aria-hidden','true');
      p.querySelectorAll('a,button,[tabindex]').forEach(function(f){if(!s)f.setAttribute('tabindex','-1');else f.removeAttribute('tabindex');});
    });
    if(focus)t.focus();
  }
  tabs.forEach(function(t,i){
    t.addEventListener('click',function(){sel(t)});
    t.addEventListener('keydown',function(e){
      var k=e.key,j=-1;
      if(k==='ArrowDown'||k==='ArrowRight')j=(i+1)%tabs.length;
      if(k==='ArrowUp'||k==='ArrowLeft')j=(i-1+tabs.length)%tabs.length;
      if(k==='Home')j=0;if(k==='End')j=tabs.length-1;
      if(j>-1){e.preventDefault();sel(tabs[j],true);}
    });
  });
  sel(tabs.filter(function(t){return t.getAttribute('aria-selected')==='true'})[0]||tabs[0]);
});

/* pause CSS loops offscreen */
function watch(el,fn,margin){
  if(!('IntersectionObserver' in window)){fn(true);return;}
  new IntersectionObserver(function(es){es.forEach(function(e){fn(e.isIntersecting);});},{rootMargin:margin||'0px'}).observe(el);
}
document.querySelectorAll('.hero,.stage').forEach(function(el){watch(el,function(v){el.classList.toggle('idle',!v);});});

/* ---------- helpers ---------- */
function typer(el){
  var nodes=[],w=document.createTreeWalker(el,NodeFilter.SHOW_TEXT),n;
  while((n=w.nextNode())){if(n.parentNode.closest('.caret,.c-p,.who'))continue;nodes.push({n:n,t:n.nodeValue});}
  var total=nodes.reduce(function(a,b){return a+b.t.length;},0),last=-1;
  function set(p){var k=Math.round(Math.max(0,Math.min(1,p))*total);if(k===last)return;last=k;
    for(var i=0;i<nodes.length;i++){var o=nodes[i],m=Math.min(k,o.t.length),v=o.t.slice(0,m);if(o.n.nodeValue!==v)o.n.nodeValue=v;k-=m;}}
  return set;
}
/* decode a value in place: key and quotes stay put, the value locks left to right
   behind an inverted cursor cell; the unlocked tail is a JSON token stream */
var TOK='0123456789abcdef0123456789abcdef"…';
function esc(s){return s.replace(/&/g,'&amp;').replace(/</g,'&lt;');}
function decoder(v){
  var t=v.textContent,q=t.length>1&&t.charAt(0)==='"'&&t.charAt(t.length-1)==='"';
  var pre=q?'"':'',post=q?'"':'',body=q?t.slice(1,-1):t,n=body.length,last='';
  function g(i,s){return TOK.charAt((i*7+s*11)%TOK.length);}
  return function(p){
    p=Math.max(0,Math.min(1,p));
    if(p>=1){if(last!=='done'){last='done';v.textContent=t;}return;}
    var lock=Math.min(n-1,Math.floor(p*n)),step=Math.floor(p*48),key=lock+'/'+step;
    if(key===last)return;last=key;
    var r='';for(var i=lock+1;i<n;i++)r+=body.charAt(i)===' '?' ':g(i,step);
    v.innerHTML=esc(pre+body.slice(0,lock))+'<i class="lk">'+esc(g(lock,step))+'</i><span class="sx">'+esc(r)+'</span>'+esc(post);
  };
}
function tween(ms,fn,done){
  var t0=0,raf=0,dead=false;
  function f(t){if(dead)return;if(!t0)t0=t;var p=Math.min(1,(t-t0)/ms);fn(p);if(p<1)raf=requestAnimationFrame(f);else if(done)done();}
  raf=requestAnimationFrame(f);
  return function(){dead=true;cancelAnimationFrame(raf);};
}
function off(el,root){var x=0,y=0;while(el&&el!==root){x+=el.offsetLeft;y+=el.offsetTop;el=el.offsetParent;}return {x:x,y:y};}
function ease(t){return t<.5?4*t*t*t:1-Math.pow(-2*t+2,3)/2;}
function lerp(a,b,t){return a+(b-a)*t;}

/* "Pick a job": switching tabs re-types each command and streams its stdout, character by character */
if(!reduce)document.querySelectorAll('#usage [role="tab"]').forEach(function(tab){
  tab.addEventListener('click',function(){replayPanel(document.getElementById(tab.getAttribute('aria-controls')));});
  tab.addEventListener('keydown',function(e){if(/^(Arrow|Home|End)/.test(e.key))setTimeout(function(){var s=document.querySelector('#usage [role="tab"][aria-selected="true"]');if(s)replayPanel(document.getElementById(s.getAttribute('aria-controls')));},0);});
});
var livePanel=null;
function replayPanel(p){
  if(!p)return;if(livePanel&&livePanel._cancel)livePanel._cancel();livePanel=p;
  var pres=[].slice.call(p.querySelectorAll('.turn pre')),i=0,dead=false,stop=null;
  pres.forEach(function(pre){if(!pre._ty){pre._n=pre.textContent.length;pre._ty=typer(pre);}pre._ty(0);pre.parentNode.classList.add('wait');pre.parentNode.classList.remove('live');});
  function next(){
    if(dead||i>=pres.length){if(window.aitsRail)aitsRail();return;}
    var pre=pres[i++],turn=pre.parentNode,who=(turn.querySelector('.who')||{}).textContent||'',out=!/^(you|stdin|text|crontab|launchd)$/i.test(who.trim());
    turn.classList.remove('wait');turn.classList.add('live',out?'out':'in');
    stop=tween(out?Math.min(720,pre._n*7):Math.min(620,pre._n*11),pre._ty,function(){turn.classList.remove('live');next();});
  }
  next();
  p._cancel=function(){dead=true;if(stop)stop();pres.forEach(function(pre){pre._ty(1);pre.parentNode.classList.remove('wait','live');});};
}
if(!reduce&&'IntersectionObserver' in window){var ud=document.querySelector('#usage .demo');if(ud){var uio=new IntersectionObserver(function(es){if(es[0].isIntersecting){uio.disconnect();replayPanel(document.querySelector('#usage .panel:not(.off)'));}},{rootMargin:'0px 0px -25% 0px'});uio.observe(ud);}}

/* =========================================================
   HERO SIGNATURE: one aits call, live from the first frame.
   ========================================================= */
var sig=document.getElementById("sig"),sigThread=null;
if(sig)(function(){
  var C1A='aits shell submit <span class="c-f">--agent</span> codex <span class="c-f">--cwd</span> ~/src/proj ';
  var REV='<span class="c-s">"Review internal/task for bugs; list them, change nothing"</span>';
  var FIX='<span class="c-s">"Fix the first bug you listed and run its tests"</span>';
  var J='<span class="c-f">--json</span> ';
  var SC={
    review:{c1:C1A+J+REV,rows:[['status','"ok"','s ok'],['agent','"codex"','s'],['session_id','"…"','sv'],['answer','"…the agent\'s review, as text…"','s'],['exit_code','0','n'],['duration_ms','81234','n']],ms:81234,exit:0,
      x2:'<span class="c-f">--write</span> '+J+FIX,
      note:'Without <code>--write</code> the agent can read but not change anything. <b>session_id</b> goes back in with <code>--session</code>.'},
    write:{c1:C1A+'<span class="c-f">--write</span> '+J+FIX,rows:[['status','"ok"','s ok'],['agent','"codex"','s'],['session_id','"…"','sv'],['answer','"…"','s'],['exit_code','0','n'],['duration_ms','12345','n']],ms:12345,exit:0,err:true,
      x2:J+'<span class="c-s">"…"</span>',
      note:'<b>--write</b> The warning goes to stderr, so stdout stays one object.'},
    timeout:{c1:C1A+'<span class="c-f">--timeout</span> 3s '+J+REV,rows:[['status','"timeout"','s bad'],['message','"…"','s'],['session_id','"…"','sv']],ms:3000,exit:1,kill:true,
      x2:J+'<span class="c-s">"…"</span>',
      note:'A timed-out result still carries <code>session_id</code>, so continue with <code>--session</code> and a narrower prompt.'},
    stop:{c1:C1A+J+REV,rows:[['status','"stopped"','s bad'],['message','"…"','s'],['session_id','"…"','sv']],exit:1,kill:true,
      x2:J+'<span class="c-s">"…"</span>',
      note:'On a timeout or a signal, aits sends TERM to the group, then KILL 5s later, and still prints the result. Every status but <code>ok</code> carries a one-line <code>message</code>.'}
  };
  var $s=function(id){return document.getElementById(id);};
  var c1=$s('sigC1'),js=$s('sigJson'),st=sig.querySelector('.sig-st b'),msEl=$s('sigMs'),err=$s('sigErr'),slot=$s('sigSlot'),x2=$s('sigX2'),note=$s('sigNote');
  var tsvg=$s('sigThread'),tpath=tsvg.querySelector('path'),tdot=tsvg.querySelector('circle');
  var keys=[].slice.call(sig.querySelectorAll('.key'));
  var tmoB=sig.querySelector('.sig-tmo b'),tmoE=sig.querySelector('.sig-tmo em');
  var busy=false,interrupt=false,cancels=[],timers=[],curRun='review',userTouched=false,chip=null,runs=0,cyc=0,sigIn=true;
  function later(ms,fn){timers.push(setTimeout(fn,ms));}
  function stopAll(){cancels.forEach(function(c){c();});cancels=[];timers.forEach(clearTimeout);timers=[];chipOff();sig.classList.remove('fly');}
  function rowsHTML(rows){
    var h='<div class="jr">{</div>';
    rows.forEach(function(r,i){h+='<div class="jr in"><span class="k">"'+r[0]+'"</span>: <span class="v '+r[2]+'"'+(r[2]==='sv'?' id="sigSv"':'')+'>'+r[1]+'</span>'+(i<rows.length-1?',':'')+'</div>';});
    return h+'<div class="jr">}</div>';
  }
  /* the trace from session_id to the NEXT slot: out past the longest line, down the card's
     right padding, and into the slot from whichever side keeps it off the text */
  function thread(p){
    var sv=$s('sigSv');if(!sv){tpath.setAttribute('d','');tdot.style.opacity=0;return;}
    var b=sig.getBoundingClientRect(),a=sv.getBoundingClientRect(),c=slot.getBoundingClientRect(),cm=$s('sigCmd2').getBoundingClientRect();
    var x1=a.right-b.left+4,y1=a.top-b.top+a.height/2,rad=10,d;
    var firstLine=c.top-cm.top<c.height*.7;
    if(firstLine){
      var right=x1;[].forEach.call(js.querySelectorAll('.jr'),function(r){var rr=document.createRange();rr.selectNodeContents(r);[].forEach.call(rr.getClientRects(),function(q){right=Math.max(right,q.right-b.left);});});
      var xr=Math.min(b.width-8,right+14),ym=cm.top-b.top-7,x2c=c.left-b.left+c.width/2,y2=c.top-b.top-1;
      if(ym-rad<y1+rad)ym=y1+2*rad;
      d='M'+x1.toFixed(1)+' '+y1.toFixed(1)+' H'+(xr-rad).toFixed(1)+' Q'+xr.toFixed(1)+' '+y1.toFixed(1)+' '+xr.toFixed(1)+' '+(y1+rad).toFixed(1)+' V'+(ym-rad).toFixed(1)+' Q'+xr.toFixed(1)+' '+ym.toFixed(1)+' '+(xr-rad).toFixed(1)+' '+ym.toFixed(1)+' H'+(x2c+rad).toFixed(1)+' Q'+x2c.toFixed(1)+' '+ym.toFixed(1)+' '+x2c.toFixed(1)+' '+Math.min(y2,ym+rad).toFixed(1)+' V'+y2.toFixed(1);
    }else{
      /* slot wrapped below the prompt line: run down the padding, under the command, up into the slot */
      var xp=b.width-Math.max(9,parseFloat(getComputedStyle(sig).paddingRight)*.45),yb=cm.bottom-b.top+5,xs=c.right-b.left-Math.min(14,c.width*.2),yS=c.bottom-b.top+1;
      d='M'+x1.toFixed(1)+' '+y1.toFixed(1)+' H'+(xp-rad).toFixed(1)+' Q'+xp.toFixed(1)+' '+y1.toFixed(1)+' '+xp.toFixed(1)+' '+(y1+rad).toFixed(1)+' V'+(yb-rad).toFixed(1)+' Q'+xp.toFixed(1)+' '+yb.toFixed(1)+' '+(xp-rad).toFixed(1)+' '+yb.toFixed(1)+' H'+(xs+rad).toFixed(1)+' Q'+xs.toFixed(1)+' '+yb.toFixed(1)+' '+xs.toFixed(1)+' '+(yb-Math.min(rad,(yb-yS)/2)).toFixed(1)+' V'+yS.toFixed(1);
    }
    tpath.setAttribute('d',d);
    var L=tpath.getTotalLength();tpath.style.strokeDasharray=L+' '+L;tpath.style.strokeDashoffset=(L*(1-p)).toFixed(1);
    var pt=tpath.getPointAtLength(L*p);tdot.setAttribute('cx',pt.x);tdot.setAttribute('cy',pt.y);tdot.style.opacity=p>0?1:0;
  }
  sigThread=thread;
  /* the session_id chip lifts out and rides the thread */
  /* the chip's own route: a bezier that lifts 40px above where it starts, arcs over the JSON and drops into the slot */
  var bz=null;
  function route(){
    var sv=$s('sigSv'),b=sig.getBoundingClientRect(),a=sv.getBoundingClientRect(),c=slot.getBoundingClientRect();
    var x0=a.left-b.left+a.width/2,y0=a.top-b.top+a.height/2,x3=c.left-b.left+c.width/2,y3=c.top-b.top+c.height/2,top=y0-40;
    bz=[x0,y0,lerp(x0,x3,.35),top-26,x3,top-26,x3,y3];
  }
  function bzAt(p){var u=1-p,a=u*u*u,b=3*u*u*p,c=3*u*p*p,d=p*p*p;return {x:a*bz[0]+b*bz[2]+c*bz[4]+d*bz[6],y:a*bz[1]+b*bz[3]+c*bz[5]+d*bz[7]};}
  function chipAt(p,txt){
    if(!chip){chip=document.createElement('span');chip.className='sig-chip';chip.setAttribute('aria-hidden','true');sig.appendChild(chip);}
    if(txt!=null)chip.textContent=txt;
    if(!bz)route();
    var pt=bzAt(p);
    var s=p<.18?1+.15*ease(p/.18):(p>.86?1.15-.15*ease((p-.86)/.14):1.15);
    chip.style.transform='translate('+(pt.x-chip.offsetWidth/2).toFixed(1)+'px,'+(pt.y-chip.offsetHeight/2).toFixed(1)+'px) scale('+s.toFixed(3)+')';
    chip.style.opacity=1;
  }
  function chipOff(){if(chip)chip.style.opacity=0;}
  function travel(done){
    var sv=$s('sigSv');if(!sv){done();return;}
    var txt=sv.textContent;route();sv.classList.add('lift');sig.classList.add('fly');slot.classList.add('await');
    cancels.push(tween(950,function(p){var q=ease(p);thread(q);chipAt(q,p===0?'session_id':null);},function(){
      chipOff();bz=null;sig.classList.remove('fly');sv.classList.remove('lift');slot.classList.remove('await','snap');
      slot.classList.add('got');void slot.offsetWidth;slot.classList.add('snap');
      done();
    }));
  }
  function press(name){keys.forEach(function(k){k.setAttribute('aria-pressed',k.getAttribute('data-run')===name?'true':'false');});}
  function idle(){runs++;if(reduce||userTouched)return;schedule(runs===1?(mobile?1500:2000):(mobile?3500:3000));}
  function schedule(ms){clearTimeout(cyc);cyc=setTimeout(function(){
    if(userTouched)return;
    if(!sigIn||!pageVisible||busy){schedule(900);return;}
    run(order[oi++%order.length]);
  },ms);}
  var order=['write','timeout','stop','review'],oi=0;
  function finish(s,anim){
    var exit=s.exit;
    sig.classList.remove('run','boot','b1');sig.classList.toggle('fail',exit!==0);
    st.textContent='exit '+exit;
    js.innerHTML=rowsHTML(s.rows);
    note.innerHTML=s.note;
    err.hidden=!s.err;
    if(!anim){thread(1);slot.classList.add('got');busy=false;return;}
    /* keys lock in one row at a time, each with a short cobalt-on-white flash */
    var rows=[].slice.call(js.querySelectorAll('.jr.in')),decs=rows.map(function(r){return decoder(r.querySelector('.v'));});
    rows.forEach(function(r){r.classList.add('pend');});decs.forEach(function(f){f(0);});
    rows.forEach(function(r,i){later(i*170,function(){
      r.classList.remove('pend');
      cancels.push(tween(160,decs[i],function(){r.classList.add('flash');later(130,function(){r.classList.remove('flash');});}));
    });});
    later(rows.length*170+240,function(){
      var sv=$s('sigSv');if(sv)sv.classList.add('ping');
      travel(function(){busy=false;idle();});
    });
  }
  function run(name,o){
    o=o||{};var s=SC[name];if(!s)return;
    stopAll();curRun=name;press(name);busy=true;interrupt=false;
    sig.classList.remove('killed','fail','b1','shockgo','tmo');sig.classList.add('run','boot');if(name==='timeout'){sig.classList.add('tmo');tmoB.style.transform='scaleX(0)';tmoE.textContent='3.0s';}
    st.textContent='running';msEl.textContent='0';
    slot.classList.remove('got','snap');thread(0);
    js.querySelectorAll('.jr').forEach(function(r){r.style.opacity='.6';});
    err.hidden=true;
    x2.innerHTML=s.x2;
    c1.innerHTML=s.c1;
    var nch=c1.textContent.length,ty=typer(c1);ty(0);
    cancels.push(tween(nch/(o.cps||80)*1000,ty,function(){
      /* the process group comes up: the wire draws, then three children pop in */
      sig.classList.add('b1');
      later(780,function(){sig.classList.remove('boot','b1');count();});
    }));
    function count(){
      var target=name==='stop'?81234:s.ms,dur=name==='timeout'?1300:1500,killAt=name==='stop'?.45:2,killed=false;
      function kill(fin){
        killed=true;stopAll();
        sig.classList.remove('shockgo');void sig.offsetWidth;sig.classList.add('shockgo');
        if(fin===SC.stop){curRun='stop';press('stop');c1.insertAdjacentHTML('beforeend','<span class="ctl"> ^C</span>');}
        sig.classList.add('killed');st.textContent=fin===SC.timeout?'timeout':'signal';
        later(750,function(){finish(fin,true);});
      }
      cancels.push(tween(dur,function(p){
        if(killed)return;
        if(p>=killAt||interrupt){kill(SC.stop);return;}
        msEl.textContent=String(Math.round(target*(name==='timeout'?p:ease(p))));
        if(name==='timeout'){tmoB.style.transform='scaleX('+p.toFixed(3)+')';tmoE.textContent=(3*(1-p)).toFixed(1)+'s';}
      },function(){
        if(killed)return;
        if(name==='timeout')kill(SC.timeout);else finish(s,true);
      }));
    }
  }
  /* first paint is the finished call; then it rewinds and plays live */
  function rewind(done){
    /* a designed 600ms rewind: lines scrub out bottom-up through VHS tracking, the counter spins back */
    busy=true;sig.classList.add('run','rw');st.textContent='running';slot.classList.remove('got');
    var ty=typer(c1),rows=[].slice.call(js.querySelectorAll('.jr')),n=rows.length;
    msEl.classList.add('spin');
    cancels.push(tween(600,function(p){
      var q=ease(p);ty(1-q);thread(Math.max(0,1-p*2.2));
      msEl.textContent=String(Math.round(81234*(1-q)));
      rows.forEach(function(r,i){
        var k=(n-1-i)/(n-1),l=Math.max(0,Math.min(1,(p-k*.55)/.45));
        r.style.opacity=String(1-.4*l);
        r.style.transform=l>0&&l<1?'translateX('+((i%2?1:-1)*(4+9*Math.abs(Math.sin(p*31+i*1.7)))).toFixed(1)+'px) skewX('+((i%2?-1:1)*6*l).toFixed(1)+'deg)':'';
      });
    },function(){msEl.classList.remove('spin');sig.classList.remove('rw');rows.forEach(function(r){r.style.transform='';});done();}));
  }
  function signal(){
    if(busy&&sig.classList.contains('run')&&!sig.classList.contains('killed')&&!sig.classList.contains('boot')){interrupt=true;}
    else run('stop');
  }
  keys.forEach(function(k){
    k.addEventListener('click',function(){
      userTouched=true;clearTimeout(cyc);var n=k.getAttribute('data-run');
      if(reduce){stopAll();curRun=n;press(n);c1.innerHTML=SC[n].c1;x2.innerHTML=SC[n].x2;sig.classList.toggle('killed',!!SC[n].kill);msEl.textContent=SC[n].ms?String(SC[n].ms):'';finish(SC[n],false);return;}
      if(n==='stop')signal();else run(n);
    });
  });
  document.addEventListener('keydown',function(e){
    if(!(e.ctrlKey&&!e.metaKey&&(e.key==='c'||e.key==='C')))return;
    var selTxt=window.getSelection&&String(window.getSelection());
    if(selTxt||!sigIn)return;
    userTouched=true;clearTimeout(cyc);
    var k=sig.querySelector('.kc');k.classList.add('down');setTimeout(function(){k.classList.remove('down');},160);
    if(reduce){k.click();return;}
    signal();
  });
  /* hover a value: it re-decodes */
  if(!reduce)js.addEventListener('pointerover',function(e){
    var v=e.target.closest&&e.target.closest('.v');if(!v||busy||v._s)return;
    v._s=1;var f=decoder(v);tween(360,f,function(){v._s=0;});
  });
  addEventListener('resize',function(){if(!busy)thread(1);});
  if(document.fonts&&document.fonts.ready)document.fonts.ready.then(function(){if(!busy)thread(1);});

  /* JSON keys: a caret block follows the pointer row by row */
  var jcar=document.createElement('i');jcar.className='jcar';jcar.setAttribute('aria-hidden','true');var hlK=null;
  if(!reduce){
    js.addEventListener('pointermove',function(e){
      var best=null,bd=1e9;[].forEach.call(js.querySelectorAll('.jr.in'),function(r){var b=r.getBoundingClientRect(),d=Math.abs(e.clientY-(b.top+b.height/2));if(d<bd){bd=d;best=r;}});
      if(!best)return;var k=best.querySelector('.k');if(!k)return;
      if(!jcar.isConnected)js.appendChild(jcar);
      jcar.style.transform='translate('+(k.offsetLeft-4)+'px,'+k.offsetTop+'px)';jcar.style.width=(k.offsetWidth+8)+'px';jcar.style.height=k.offsetHeight+'px';jcar.classList.add('on');
      if(hlK!==k){if(hlK)hlK.classList.remove('hl');k.classList.add('hl');hlK=k;}
    });
    js.addEventListener('pointerleave',function(){jcar.classList.remove('on');if(hlK){hlK.classList.remove('hl');hlK=null;}});
  }
  /* pointer tilt: the card leans (max 4deg); the JSON and the process group sit at different depths */
  if(!reduce&&!mobile&&matchMedia('(hover:hover) and (pointer:fine)').matches){
    var proc=$s('sigProc'),tx=0,tyy=0,gx=0,gy=0,traf=0;
    var tick8=function(){
      traf=0;tx+=(gx-tx)*.14;tyy+=(gy-tyy)*.14;
      sig.style.transform='perspective(1600px) rotateX('+(-tyy*8).toFixed(2)+'deg) rotateY('+(tx*8).toFixed(2)+'deg)';
      js.style.transform='translate3d('+(-tx*14).toFixed(1)+'px,'+(-tyy*8).toFixed(1)+'px,0)';
      proc.style.transform='translate3d('+(tx*18).toFixed(1)+'px,'+(tyy*10).toFixed(1)+'px,0)';
      if(!busy&&!sig.classList.contains('fly'))thread(1);
      if(Math.abs(gx-tx)>.002||Math.abs(gy-tyy)>.002)traf=requestAnimationFrame(tick8);
    };
    sig.addEventListener('pointermove',function(e){var r=sig.getBoundingClientRect();gx=Math.max(-.5,Math.min(.5,(e.clientX-r.left)/r.width-.5));gy=Math.max(-.5,Math.min(.5,(e.clientY-r.top)/r.height-.5));if(!traf)traf=requestAnimationFrame(tick8);});
    sig.addEventListener('pointerleave',function(){gx=gy=0;if(!traf)traf=requestAnimationFrame(tick8);});
  }
  thread(1);
  if(!reduce){
    watch(sig,function(v){sigIn=v;});
    /* play the intro where it can be seen: at once on a wide screen, on arrival below the fold on a phone */
    /* the loop opens on its payoff: within 600ms session_id flies into the next command, then the call rewinds and types */
    var introDone=false,intro=function(){if(introDone||userTouched)return;introDone=true;busy=true;
      travel(function(){later(650,function(){if(userTouched){busy=false;return;}rewind(function(){run('review',{cps:90});});});});};
    if(sig.getBoundingClientRect().top<innerHeight*.75||!('IntersectionObserver' in window))setTimeout(intro,350);
    else{var iio=new IntersectionObserver(function(es){if(es[0].isIntersecting){iio.disconnect();intro();}},{threshold:.35});iio.observe(sig);}
  }
})();

/* page thread: from the hero's --session slot down through every numbered section */
if(window.aitsThread){
  var pageThread=aitsThread({
    start:function(){return document.getElementById('sigSlot');},
    x:function(){var w=document.querySelector('#commands .wrap');var r=w.getBoundingClientRect(),pl=parseFloat(getComputedStyle(w).paddingLeft)||16;return r.left+scrollX+Math.max(7,pl*.42);},
    bend:function(){var c=document.querySelector('.hero .cue');var r=c.getBoundingClientRect();return r.bottom+scrollY+8;},
    nodes:function(){return [].slice.call(document.querySelectorAll('main section:not(.hero) .num'));},
    endEl:function(){return document.querySelector('.close .h-sq');},
    at:.66,drop:110,
    onDraw:function(x,y){if(window.aitsRail)aitsRail(x,y);}
  });
}

/* strike through what aits is not */
var no=document.querySelector('.isnot .no');
if(no){if(reduce)no.classList.add('lit');else watch(no,function(v){if(v)no.classList.add('lit');},'0px 0px -20% 0px');}

/* session search scanner */
var sb=document.getElementById('scanbody');
if(sb){
  var track=document.createElement('div');track.className='scan-track';
  var terms=['process','group','timeout'],seed=7,rnd=function(){seed=(seed*9301+49297)%233280;return seed/233280;},rws=[];
  for(var i=0;i<22;i++){var hit=i%7===3,h='<em>'+(hit?'match':'session')+'</em>',n=3+Math.floor(rnd()*4);
    for(var k=0;k<n;k++){h+='<i style="width:'+(18+Math.floor(rnd()*80))+'px"></i>';if(hit&&k<3)h+='<u>'+terms[k]+'</u>';}
    rws.push('<div class="srow'+(hit?' hit':'')+'">'+h+'</div>');}
  track.innerHTML=rws.join('')+rws.join('');sb.appendChild(track);
  var beam=document.createElement('div');beam.className='scan-beam';sb.appendChild(beam);
  if(!reduce){
    var on=false,raf=0,y=0,last=0;
    var step=function(t){raf=0;if(!on||!pageVisible)return;var dt=Math.min(50,t-(last||t));last=t;y=(y+dt*.03)%(track.scrollHeight/2||1);track.style.transform='translate3d(0,'+(-y).toFixed(1)+'px,0)';raf=requestAnimationFrame(step);};
    var go=function(){if(on&&pageVisible&&!raf){last=0;raf=requestAnimationFrame(step);}};
    watch(sb,function(v){on=v;go();});
    document.addEventListener('visibilitychange',go);
  }
}

if(reduce||!window.gsap||!window.ScrollTrigger){
  /* static film: the codex exec node still shows the agent's output, not an empty box */
  var st0=document.getElementById('stream');
  if(st0&&!st0.firstChild){
    var SL=[['$ aits shell submit --agent codex','#ffb39d'],['  --cwd ~/src/proj --json','#9db0ff'],['codex exec  read-only','#ff4d1f'],['{','#8a8ea0'],['  "status": "ok",','#e9e7df'],['  "agent": "codex",','#e9e7df'],['  "session_id": "…",','#ff4d1f'],['  "exit_code": 0, …','#ffd2c4']];
    SL.forEach(function(l,i){var t=document.createElementNS('http://www.w3.org/2000/svg','text');
      t.setAttribute('x',468);t.setAttribute('y',248+9+i*15);t.setAttribute('class','lg');t.setAttribute('fill',l[1]);t.textContent=l[0];st0.appendChild(t);});
  }
  return;
}
R.classList.add('fx');
gsap.registerPlugin(ScrollTrigger);

/* ---------- hero headline: ink pressing in, letters that stretch under the pointer ---------- */
var h1=document.getElementById('h1'),chs=[];
h1.querySelectorAll('.ln').forEach(function(ln){
  var t=ln.firstChild;if(!t||t.nodeType!==3)return;
  var frag=document.createDocumentFragment();
  t.nodeValue.split(/(\s+)/).forEach(function(word){
    if(!word)return;
    if(/^\s+$/.test(word)){frag.appendChild(document.createTextNode(word));return;}
    var w=document.createElement('span');w.style.whiteSpace='nowrap';w.style.display='inline-block';
    word.split('').forEach(function(c){var s=document.createElement('span');s.className='ch';s.textContent=c;w.appendChild(s);chs.push(s);});
    frag.appendChild(w);
  });
  ln.replaceChild(frag,t);
});
var cur=chs.map(function(){return {w:72,g:880};});
function paint(){for(var i=0;i<chs.length;i++){chs[i].style.setProperty('--w',cur[i].w.toFixed(1));chs[i].style.setProperty('--g',cur[i].g.toFixed(0));}}
var intro=gsap.fromTo(cur,{w:84,g:300},{w:72,g:880,duration:1.3,ease:'expo.out',stagger:.022,onUpdate:paint});
paint();
var centers=[],loop=0,mx=-1e4,my=-1e4;
function measure(){var r=h1.getBoundingClientRect();centers=chs.map(function(c){var b=c.getBoundingClientRect();return [b.left+b.width/2-r.left,b.top+b.height/2-r.top];});}
function tick(){
  loop=0;var r=h1.getBoundingClientRect(),moving=false,rad=Math.max(110,r.width*.2);
  for(var i=0;i<chs.length;i++){
    var dx=mx-r.left-centers[i][0],dy=my-r.top-centers[i][1],d=Math.sqrt(dx*dx+dy*dy);
    var f=Math.max(0,1-d/rad);f=f*f*(3-2*f);
    var tw=72+26*f,tg=880-460*f;
    cur[i].w+=(tw-cur[i].w)*.18;cur[i].g+=(tg-cur[i].g)*.18;
    if(Math.abs(tw-cur[i].w)>.2||Math.abs(tg-cur[i].g)>1)moving=true;
  }
  paint();if(moving)loop=requestAnimationFrame(tick);
}
if(!mobile&&matchMedia('(hover:hover)').matches){
  intro.eventCallback('onComplete',function(){
    measure();
    h1.addEventListener('pointermove',function(e){mx=e.clientX;my=e.clientY;if(!loop)loop=requestAnimationFrame(tick);});
    h1.addEventListener('pointerleave',function(){mx=my=-1e4;if(!loop)loop=requestAnimationFrame(tick);});
    addEventListener('resize',measure);
  });
}

/* ---------- the film ---------- */
var stage=document.getElementById('stage');
var $=function(q){return stage.querySelector(q);},$$=function(q){return [].slice.call(stage.querySelectorAll(q));};
var sc=$$('.scene'),s1=sc[0],s2=sc[1],s4=sc[2],s5=sc[3],s6=sc[4],s8=sc[5];
var iris=$('.iris'),shutter=$('.shutter'),trail=$('#trail'),tp=trail.querySelector('path');
tp.setAttribute('pathLength','1');tp.style.strokeDasharray='1 1';tp.style.strokeDashoffset='1';
gsap.set(sc,{autoAlpha:0});gsap.set(s1,{autoAlpha:1});
gsap.set($$('.cap'),{autoAlpha:0});gsap.set(s1.querySelector('.cap'),{autoAlpha:1});
gsap.set(iris,{scale:0});gsap.set(shutter,{yPercent:101});gsap.set(trail,{opacity:0});

var END=27.6;
var RESTS=[0,2.6,5.7,8.6,12.95,16.05,17.75,20.45,24.25,26.6,END];
var tl=gsap.timeline({paused:true,defaults:{ease:'power2.inOut'}});
function cap(el,a,b){tl.fromTo(el,{autoAlpha:0,y:18},{autoAlpha:1,y:0,duration:.3,ease:'power3.out',immediateRender:false},a);tl.to(el,{autoAlpha:0,y:-12,duration:.25},b);}
function prox(fn,a,d,ez){var o={p:0};tl.fromTo(o,{p:0},{p:1,duration:d,ease:ez||'none',immediateRender:false,onUpdate:function(){fn(o.p);}},a);}
function sceneIn(el,a,from){tl.fromTo(el,Object.assign({autoAlpha:1},from||{}),{scale:1,x:0,y:0,yPercent:0,duration:.6,ease:'power3.out',immediateRender:false},a);}

/* 01 type: the command is already half typed when the frame opens */
var cmd1=$('#cmd1'),t1=typer(cmd1),full1=cmd1.textContent,caps=$$('.keyrow .kcap'),litK=null;
var capR=caps.map(function(c){var tok=c.getAttribute('data-t');if(tok==='RET')return [full1.length,full1.length+1];var i=full1.indexOf(tok);return [i,c.classList.contains('kp')?full1.length-1:i+tok.length];});
function type1(p){t1(p);var k=Math.round(Math.max(0,Math.min(1,p))*(full1.length-2))+2,hit=null;
  for(var i=0;i<caps.length;i++){var r=capR[i];if(r[0]>-1&&k>r[0]&&k<=r[1]+1)hit=caps[i];}
  if(p>=.999)hit=caps[caps.length-1];
  if(hit!==litK){if(litK)litK.classList.remove('on');if(hit)hit.classList.add('on');litK=hit;}}
type1(.4);
prox(function(p){type1(.4+.6*p);},.05,1.9);
tl.to($('.keyrow'),{autoAlpha:0,y:20,duration:.3,ease:'power2.in'},2.75);
tl.from(s1.querySelectorAll('.tg'),{autoAlpha:0,scale:.6,y:20,duration:.35,stagger:.2,ease:'back.out(2)'},1.9);
tl.to(s1.querySelector('.cap'),{autoAlpha:0,duration:.25},2.75);
tl.to(s1.querySelector('.body'),{scale:.32,xPercent:-38,yPercent:-4,autoAlpha:0,duration:.7,ease:'power3.in'},2.75);
tl.to(s1.querySelector('.st'),{yPercent:-120,autoAlpha:0,duration:.4,ease:'power2.in'},2.8);
tl.set(s1,{autoAlpha:0},3.5);

/* 02 spawn */
var svg=$('#pg'),cam=$('#cam');
/* phones: the diagram runs top to bottom (aits, then the group, then three children stacked) */
var VERT=matchMedia('(max-width:600px)').matches;
var PG={aits:'140 300',grp:'750 300',codex:'590 300',cx:590,cy:300,vw:1200,vh:600,sx:468,sy:248};
if(VERT){
  var SA=function(q,o){var e=svg.querySelector(q);for(var k in o)e.setAttribute(k,o[k]);};
  svg.setAttribute('viewBox','0 0 600 940');cam.classList.add('vert');
  SA('.n-aits rect',{x:200,y:24,width:200,height:96});SA('.n-aits .tl',{x:300,y:72});SA('.n-aits .ts',{x:300,y:100});
  SA('.w0',{d:'M300 120 V 290'});
  SA('.gbox',{x:24,y:180,width:552,height:744});SA('.tg1',{x:52,y:224});SA('.tg2',{x:548,y:902});
  SA('.n-codex rect.node',{x:150,y:290,width:300,height:160});SA('#codexClip rect',{x:162,y:302,width:276,height:136});SA('.tl-up',{x:300,y:280});
  SA('.ro rect',{x:202,y:466});SA('.ro text',{x:300,y:493});
  SA('.w1',{d:'M150 370 C 74 370, 74 590, 110 590'});SA('.w2',{d:'M150 370 C 62 370, 62 710, 110 710'});SA('.w3',{d:'M150 370 C 50 370, 50 830, 110 830'});
  [590,710,830].forEach(function(cy,i){SA('.k'+(i+1)+' rect',{x:110,y:cy-40,width:400,height:80});SA('.k'+(i+1)+' text',{x:310,y:cy+9});});
  PG={aits:'300 72',grp:'300 550',codex:'300 370',cx:300,cy:370,vw:600,vh:940,sx:178,sy:318};
}
var wires=$$('.pg .wire');
wires.forEach(function(p){var L=p.getTotalLength();p.style.strokeDasharray=L;p.style.strokeDashoffset=L;});
gsap.set(['.n-aits','.grp','.n-codex','.kid','.ro','.tg2'].map(function(q){return svg.querySelectorAll(q);}),{autoAlpha:0});
gsap.set([s2.querySelector('.st-b'),s2.querySelector('.meter')],{autoAlpha:0});
tl.set(s2,{autoAlpha:1},2.9);
/* the cut into Spawn is the process group's own shape: a dashed rounded box opens out to the full frame */
var wipe=document.createElement('div');wipe.className='wipe';wipe.setAttribute('aria-hidden','true');s2.appendChild(wipe);
gsap.set(wipe,{autoAlpha:0});
tl.fromTo(s2,{clipPath:'inset(36% 30% 36% 30% round 26px)'},{clipPath:'inset(0% 0% 0% 0% round 0px)',duration:.6,ease:'power3.inOut',immediateRender:false},2.9);
tl.fromTo(wipe,{autoAlpha:1,top:'36%',bottom:'36%',left:'30%',right:'30%'},{top:'0%',bottom:'0%',left:'0%',right:'0%',duration:.6,ease:'power3.inOut',immediateRender:false},2.9);
tl.to(wipe,{autoAlpha:0,duration:.2},3.45);
tl.from(s2.querySelector('.st-a'),{yPercent:110,autoAlpha:0,duration:.45,ease:'power3.out'},3.0);
tl.fromTo(svg.querySelector('.n-aits'),{autoAlpha:0,scale:.4,x:VERT?0:300,y:VERT?160:0,svgOrigin:PG.aits},{autoAlpha:1,scale:1,x:0,y:0,duration:.6,ease:'power3.out',immediateRender:false},2.95);
tl.to(svg.querySelector('.w0'),{strokeDashoffset:0,duration:.4,ease:'none'},3.45);
/* the group's dashed border draws itself round, dash by dash (the dashes stay put while the line grows) */
var gbox=svg.querySelector('.gbox'),gL=0;
function gdraw(p){
  if(!gL)gL=gbox.getTotalLength();
  if(p>=1){gbox.style.strokeDasharray='';return;}
  var n=Math.floor(p*gL/24),a=[];for(var i=0;i<n;i++)a.push(14,10);var rest=p*gL-n*24;a.push(Math.min(14,rest),Math.max(10,24-Math.min(14,rest)),0,gL+30);
  gbox.style.strokeDasharray=a.join(' ');
}
gdraw(0);
tl.fromTo(svg.querySelector('.grp'),{autoAlpha:0},{autoAlpha:1,duration:.01,immediateRender:false},3.7);
tl.fromTo(gbox,{fillOpacity:0},{fillOpacity:1,duration:.4,immediateRender:false},4.0);
tl.fromTo(svg.querySelector('.tg1'),{autoAlpha:0,x:-16},{autoAlpha:1,x:0,duration:.3,immediateRender:false},3.85);
prox(gdraw,3.7,.6,'power2.inOut');
tl.fromTo(svg.querySelector('.n-codex'),{autoAlpha:0,scale:.5,svgOrigin:PG.codex},{autoAlpha:1,scale:1,duration:.5,ease:'back.out(1.8)',immediateRender:false},4.05);
tl.to(svg.querySelectorAll('.w1,.w2,.w3'),{strokeDashoffset:0,duration:.45,stagger:.12,ease:'none'},4.55);
/* fork: each child pops off the wire with a spring overshoot */
tl.fromTo(svg.querySelectorAll('.kid'),{autoAlpha:0,x:-40,scale:.35,transformOrigin:'0% 50%'},{autoAlpha:1,x:0,scale:1,duration:.45,stagger:.12,ease:'back.out(2)',immediateRender:false},4.85);
tl.fromTo(svg.querySelector('.tg2'),{autoAlpha:0,x:20},{autoAlpha:1,x:0,duration:.35,immediateRender:false},5.4);
cap(s2.querySelector('.cap-a'),3.15,5.85);

/* 03 work: push the camera into the agent */
var stream=$('#stream'),NS='http://www.w3.org/2000/svg';
/* the agent's own output scrolls inside codex exec: the same command and result lines as the hero card,
   set in mono, so the dive flies through real text into the JSON */
var LOG=[['$ aits shell submit --agent codex','p'],['  --cwd ~/src/proj --json','f'],['"Review internal/task for bugs;','s'],[' list them, change nothing"','s'],['','x'],
  ['codex exec  read-only','k'],['cwd ~/src/proj','x'],['','x'],['{','x'],['  "status": "ok",','j'],['  "agent": "codex",','j'],['  "session_id": "…",','h'],['  "answer": "…the agent\'s review…",','j'],['  "exit_code": 0,','n'],['  "duration_ms": 81234','n'],['}','x']];
var LC={p:'#ffb39d',f:'#9db0ff',s:'#7fe0b4',k:'#ff4d1f',x:'#8a8ea0',j:'#e9e7df',h:'#ff4d1f',n:'#ffd2c4'};
for(var i=0;i<34;i++){var L0=LOG[i%LOG.length],r=document.createElementNS(NS,'text');
  r.setAttribute('x',PG.sx);r.setAttribute('y',PG.sy+9+i*17);r.setAttribute('class','lg');r.setAttribute('fill',LC[L0[1]]);
  r.textContent=L0[0]||' ';stream.appendChild(r);}
tl.to(s2.querySelector('.st-a'),{yPercent:-110,autoAlpha:0,duration:.35},5.95);
tl.fromTo(s2.querySelector('.st-b'),{yPercent:110,autoAlpha:0},{yPercent:0,autoAlpha:1,duration:.4,ease:'power3.out',immediateRender:false},6.05);
gsap.set(cam,{transformOrigin:'50% 50%'});
/* the push-in is sized from the viewport: the Work diagram (svg x 106..1104, y 68..532 once the nodes have moved)
   is scaled and centred to fit between the duration plate and the caption, with a 56px safe margin at the sides */
function workCam(k){
  var w=cam.offsetWidth,h=cam.offsetHeight,o=off(cam,stage),SW=stage.clientWidth,SH=stage.clientHeight,M=56;
  var bx0=106/1200*w,bx1=1104/1200*w,by0=68/600*h,by1=532/600*h;
  var mt=s2.querySelector('.meter'),cp=s2.querySelector('.cap-b');
  var top=Math.max(M+40,mt&&mt.offsetParent?mt.offsetTop+mt.offsetHeight+14:0),bot=SH-Math.max(M,cp?SH-cp.offsetTop+14:M);
  var sc=Math.min(1.25,(SW-2*M)/(bx1-bx0),(bot-top)/(by1-by0));
  var ccx=(bx0+bx1)/2,ccy=(by0+by1)/2,ox=w/2,oy=h/2;
  var x=SW/2-(o.x+ox+(ccx-ox)*sc),y=(top+bot)/2-(o.y+oy+(ccy-oy)*sc);
  return k==='s'?sc:k==='x'?x:y;
}
if(VERT||mobile)tl.to(cam,{scale:VERT?1.08:1.2,duration:1.1,ease:'power2.inOut'},6.0);
else tl.to(cam,{scale:function(){return workCam('s');},x:function(){return workCam('x');},y:function(){return workCam('y');},duration:1.1,ease:'power2.inOut'},6.0);
if(!VERT){
tl.to(svg.querySelectorAll('.kid,.tg2'),{x:-60,duration:1.1,ease:'power2.inOut'},6.0);
tl.to(svg.querySelector('.n-aits'),{x:70,duration:1.1,ease:'power2.inOut'},6.0);
tl.to(svg.querySelector('.w0'),{attr:{d:'M310 300 H 440'},duration:1.1,ease:'power2.inOut'},6.0);
tl.to(svg.querySelector('.gbox'),{attr:{width:770},duration:1.1,ease:'power2.inOut'},6.0);
}
tl.fromTo(stream,{y:0},{y:-430,duration:3.2,ease:'none',immediateRender:false},6.0);
tl.fromTo(svg.querySelector('.ro'),{autoAlpha:0,y:16},{autoAlpha:1,y:0,duration:.3,ease:'back.out(2)',immediateRender:false},6.4);
tl.fromTo(s2.querySelector('.meter'),{autoAlpha:0,y:-16},{autoAlpha:1,y:0,duration:.4,ease:'power3.out',immediateRender:false},6.2);
var dur=$('#dur');prox(function(p){dur.textContent=Math.round(81234*p);},6.3,2.4);
cap(s2.querySelector('.cap-b'),6.3,8.75);

/* 04 assemble: dive through the agent; a cobalt iris opens and the result builds */
tl.to(s2.querySelector('.meter'),{autoAlpha:0,duration:.3},8.85);
tl.to(s2.querySelector('.st-b'),{autoAlpha:0,duration:.3},8.85);
function diveXY(axis){
  var o=off(cam,stage),w=cam.offsetWidth,h=cam.offsetHeight,ox=.5*w,oy=.5*h,cx=PG.cx/PG.vw*w,cy=PG.cy/PG.vh*h;
  return axis==='x'?stage.clientWidth/2-o.x-ox-(cx-ox)*9:stage.clientHeight/2-o.y-oy-(cy-oy)*9;
}
tl.to(cam,{scale:9,x:function(){return diveXY('x');},y:function(){return diveXY('y');},duration:1,ease:'power3.in'},8.9);
gsap.set(cam,{filter:'blur(0px)'});
tl.to(cam,{filter:'blur(4px)',duration:.3,ease:'power2.in'},9.6);
tl.fromTo(iris,{scale:1,autoAlpha:0},{scale:1,autoAlpha:1,duration:.32,ease:'power1.in',immediateRender:false},9.58);
tl.set(cam,{filter:'none'},10.3);
tl.set(s4,{autoAlpha:1},10.0);
tl.set(s2,{autoAlpha:0},10.3);
tl.from(s4.querySelector('.st'),{yPercent:110,autoAlpha:0,duration:.4,ease:'power3.out'},9.8);
var jx=$('#jx'),rows=[].slice.call(jx.querySelectorAll('.jr')),rowLen=rows.map(function(r){return r.textContent.length;});
jx.style.width=(Math.max.apply(null,rowLen)+2)+'ch';
gsap.set(rows,{autoAlpha:0,transformOrigin:'0 0'});
/* the raw stdout line runs across the top, then unzips: each key/value flies down to its row */
var raw=$('#raw'),rq,rts=[];
(function(){
  var h='<span class="lt">stdout</span><span class="rq"><span class="rb">{</span>';
  for(var k=1;k<rows.length-1;k++){var t=rows[k].textContent.replace(/^\s+/,'').replace(/: /,':');h+='<span class="rt">'+esc(t)+'</span>';}
  raw.innerHTML=h+'<span class="rb">}</span></span>';
  rq=raw.querySelector('.rq');rts=[].slice.call(raw.querySelectorAll('.rt'));
})();
gsap.set(raw,{autoAlpha:0});
tl.fromTo(raw,{autoAlpha:0},{autoAlpha:1,duration:.2,immediateRender:false},9.95);
function rawX(t){var p=Math.max(0,Math.min(1,(t-9.95)/.7));return (1-ease(p))*stage.clientWidth*.9;}
prox(function(p){rq.style.transform='translate3d('+rawX(9.95+p*.7).toFixed(1)+'px,0,0)';},9.95,.7);
tl.fromTo(rows[0],{autoAlpha:0},{autoAlpha:1,duration:.15,immediateRender:false},10.2);
/* numbers are frozen while in flight: blank until the row lands, then the real value. Never a different digit. */
function odo(v,target){
  var t=String(target),last=null;
  return function(p){var on=p>=1;if(on===last)return;last=on;
    v.textContent=on?t:'\u00a0'.repeat(t.length);v.classList.toggle('rolling',!on);
    if(on){v.classList.remove('landed');void v.offsetWidth;v.classList.add('landed');}
  };
}
for(var k=1;k<rows.length-1;k++){(function(row,k){
  var a=10.25+(k-1)*.4,d=.6,tok=rts[k-1],val=row.querySelector('.v.n'),roll=val?odo(val,parseInt(val.textContent,10)):null;
  if(roll)roll(0);
  prox(function(p){
    var t=a+p*d,q=Math.max(0,Math.min(1,p));
    var ro=off(row,s4),to=off(tok,s4);
    var tx=to.x+rawX(t),ty=to.y,sc0=tok.offsetHeight/Math.max(1,row.offsetHeight);
    var ex=1-Math.pow(1-q,3),ey=q*q*(3-2*q)*q;          /* x leads, y falls late: a curved drop */
    var x=lerp(tx-ro.x,0,ex),y=lerp(ty-ro.y,0,Math.min(1,ey*1.25)),sc=lerp(sc0,1,ease(q));
    gsap.set(row,{autoAlpha:q>0?1:0,x:x,y:y,scale:sc});
    tok.style.opacity=String(1-.88*Math.min(1,q*2));
    if(roll)roll(q>=.97?1:0);
  },a,d);
})(rows[k],k);}
tl.fromTo(rows[rows.length-1],{autoAlpha:0},{autoAlpha:1,duration:.15,immediateRender:false},12.75);
tl.to(raw,{autoAlpha:0,duration:.3},13.1);
cap(s4.querySelector('.cap-a'),10.0,11.35);
cap(s4.querySelector('.cap-b'),12.3,13.0);
cap(s4.querySelector('.cap-c'),13.1,14.3);
/* collapse: indented for reading, printed as one line (phones keep it indented, at a size you can read) */
function charW(){var p=document.createElement('span');p.textContent='0000000000';p.style.cssText='position:absolute;visibility:hidden;white-space:pre';jx.appendChild(p);var w=p.offsetWidth/10;jx.removeChild(p);return w;}
var geo=null;
function G2(){
  if(geo&&geo.vw===innerWidth&&geo.vh===innerHeight)return geo;
  var cw=charW(),cum=0,xs=[];
  rows.forEach(function(r,i){var ind=r.classList.contains('in')?2*cw:0;xs.push({x:cum*cw-ind,y:-r.offsetTop});cum+=rowLen[i];});
  var total=cum*cw,z=Math.min(1,stage.clientWidth*.92/total),o=off(jx,s4);
  geo={vw:innerWidth,vh:innerHeight,xs:xs,z:z,cx:(stage.clientWidth-total*z)/2-o.x,cy:stage.clientHeight*.38-o.y};return geo;
}
/* FLIP, in two moves. x first: each row slides out to its column in the printed line, one after another,
   so the object opens into a clean staircase (every row keeps its own line). Then y: the steps drop onto
   one line together, in the last ~15% of the move. Columns never overlap, so nothing overprints. */
if(!mobile){
  var CX0=13.1,CXS=.06,CXD=.42,CY0=CX0+(rows.length-1)*CXS+CXD,CYD=.15;
  rows.forEach(function(r,i){
    tl.to(r,{x:function(){return G2().xs[i].x;},duration:CXD,ease:'power3.inOut'},CX0+i*CXS);
    tl.to(r,{y:function(){return G2().xs[i].y;},duration:CYD,ease:'power3.inOut'},CY0+i*.004);
  });
  tl.to(jx,{scale:function(){return G2().z;},x:function(){return G2().cx;},y:function(){return G2().cy;},duration:CY0-CX0,ease:'power3.inOut'},CX0);
  tl.to(s4.querySelector('.st'),{autoAlpha:0,yPercent:-100,duration:.25},CY0+CYD+.02);
}else tl.to(s4.querySelector('.st'),{autoAlpha:0,yPercent:-100,duration:.3},13.3);
ScrollTrigger.addEventListener('refreshInit',function(){geo=null;});

/* 05 hand off: the session id leaves the result, trailing the page's thread, and becomes the next call's --session */
var sv=$('#sv'),slot=$('#slot'),flyer=$('#flyer'),marq=$('#marq'),cmd2=$('#cmd2');
tl.to(sv,{scale:1.35,duration:.1,ease:'power2.out'},14.1).to(sv,{scale:1,duration:.1},14.2);
gsap.set(flyer,{autoAlpha:0});gsap.set(marq,{yPercent:-50});
tl.set(flyer,{autoAlpha:1},14.3);tl.set(sv,{autoAlpha:0},14.3);
function threadX(){var w=document.querySelector('#commands .wrap');var pl=parseFloat(getComputedStyle(w).paddingLeft)||16;return w.getBoundingClientRect().left+Math.max(7,pl*.42)-stage.getBoundingClientRect().left;}
function backOut(t){var c=2.4;return 1+(c+1)*Math.pow(t-1,3)+c*Math.pow(t-1,2);}
/* two beats. 1: the chip lifts out of the result, grows to 1.6x and arcs over to park beside the command.
   2: it shrinks into the dashed <session_id> slot with an overshoot. At rest it never covers command text. */
var A0=null;
function fly(p){
  flyer.style.fontSize=getComputedStyle(cmd2).fontSize;
  var sr=stage.getBoundingClientRect(),fw=flyer.offsetWidth,fh=flyer.offsetHeight,c=slot.getBoundingClientRect(),cm=cmd2.getBoundingClientRect();
  if(!A0||p<.02){var a=sv.getBoundingClientRect();A0={x:a.left+a.width/2-sr.left,y:a.top+a.height/2-sr.top,s:Math.max(.2,a.height/fh)};}
  var A=A0,base=1;
  var C={x:c.left+c.width/2-sr.left,y:c.top+c.height/2-sr.top,s:Math.max(.5,c.height/fh)};
  var top=cm.top-sr.top,B;
  if(!mobile){var hw=fw*1.6/2,mw=parseFloat(getComputedStyle(cmd2).maxWidth)||cm.width,cr=cm.left-sr.left+Math.min(mw,sr.width*.7);
    B={x:Math.min(sr.width-hw-28,cr+Math.max(hw+36,(sr.width-cr)/2)),y:C.y,s:1.6};}
  else B={x:sr.width/2,y:top-fh*1.3/2-26,s:1.3};
  var x,y,sc,rot=0,tw=0;
  if(p<=.5){var q=ease(p/.5);x=lerp(A.x,B.x,q);y=lerp(A.y,B.y,q)-Math.sin(q*Math.PI)*(mobile?60:130);sc=lerp(A.s,B.s,q);rot=-Math.sin(q*Math.PI)*5;tw=0;}
  else{var u=(p-.5)/.5,q2=ease(u);x=lerp(B.x,C.x,q2);y=lerp(B.y,C.y,q2)-Math.sin(q2*Math.PI)*70;sc=lerp(B.s,C.s,backOut(Math.min(1,u*1.08)));rot=Math.sin(q2*Math.PI)*4;tw=Math.min(1,u*2.2);}
  flyer.style.transform='translate('+(x-fw/2).toFixed(1)+'px,'+(y-fh/2).toFixed(1)+'px) scale('+sc.toFixed(3)+') rotate('+rot.toFixed(2)+'deg)';
  /* the ghost of the line rides above the command, faded at both edges */
  marq.style.top=Math.max(96,top-(mobile?70:84)).toFixed(0)+'px';
  /* the page's thread comes down on the left, runs above the command and drops to the chip from above */
  var tx=threadX(),ry=Math.max(70,top-30),r=16,ey=y-fh*sc/2-2,d;
  if(ey>ry+r&&x>tx+3*r)d='M'+tx.toFixed(1)+' -20 V'+(ry-r).toFixed(1)+' Q'+tx.toFixed(1)+' '+ry.toFixed(1)+' '+(tx+r).toFixed(1)+' '+ry.toFixed(1)+' H'+(x-r).toFixed(1)+' Q'+x.toFixed(1)+' '+ry.toFixed(1)+' '+x.toFixed(1)+' '+(ry+r).toFixed(1)+' V'+ey.toFixed(1);
  else{var yy=Math.min(ey,ry);d='M'+tx.toFixed(1)+' -20 C '+tx.toFixed(1)+' '+yy.toFixed(1)+', '+(x-160).toFixed(1)+' '+yy.toFixed(1)+', '+x.toFixed(1)+' '+yy.toFixed(1);}
  tp.setAttribute('d',d);
  tp.style.strokeDashoffset=Math.max(0,1-p*2.2).toFixed(3);
  tp.style.opacity=String(1-tw);
}
var fo={p:0};
tl.fromTo(fo,{p:0},{p:.5,duration:.75,ease:'none',immediateRender:false,onUpdate:function(){fly(fo.p);}},14.3);
tl.to(fo,{p:.5,duration:1.05,ease:'none',onUpdate:function(){fly(fo.p);}},15.05);
tl.to(fo,{p:1,duration:.7,ease:'none',onUpdate:function(){fly(fo.p);}},16.1);
tl.fromTo(trail,{opacity:0},{opacity:1,duration:.2,immediateRender:false},14.3);
tl.to(trail,{opacity:0,duration:.3},17.95);
if(!mobile){
  tl.fromTo(marq,{opacity:0,xPercent:0},{opacity:.2,xPercent:-6,duration:.5,ease:'power2.out',immediateRender:false},14.35);
  tl.to(marq,{xPercent:-34,duration:2.0,ease:'none'},14.85);
  tl.to(marq,{opacity:0,duration:.35},16.75);
}
tl.set(s5,{autoAlpha:1},14.85);
tl.to(iris,{scale:0,duration:.5,ease:'power2.in'},14.85);
tl.to(s4,{autoAlpha:0,duration:.3},14.9);
gsap.set(s5.querySelectorAll('.lane'),{autoAlpha:0});
var t2=typer($('#cmd2'));t2(0);
tl.from(s5.querySelector('.st'),{yPercent:110,autoAlpha:0,duration:.4,ease:'power3.out'},15.2);
prox(t2,15.15,.85);
tl.set(cmd2,{className:'cmd mid dim'},15.2);
tl.set(cmd2,{className:'cmd mid'},16.8);
tl.set(flyer,{autoAlpha:0},16.8);
tl.set(slot,{className:'slot got'},16.8);
tl.set(slot,{className:'slot got flash'},16.8);
tl.set(slot,{className:'slot got'},16.85);
tl.fromTo(slot,{scale:1.5},{scale:1,duration:.35,ease:'back.out(3)',immediateRender:false},16.8);
tl.fromTo(cmd2,{x:0},{keyframes:[{x:4,duration:.04},{x:-4,duration:.05},{x:3,duration:.04},{x:-2,duration:.04},{x:0,duration:.04}],immediateRender:false},16.81);
/* the thread pulls taut: from the slot straight down toward the page's own line */
prox(function(p){
  var sr=stage.getBoundingClientRect(),c=slot.getBoundingClientRect(),x=c.left+c.width/2-sr.left,y=c.top+c.height-sr.top,tx=threadX(),H=sr.height+20,q=ease(p);
  /* out of the slot to the left, into the gutter, then straight down: clear of the command text */
  tx=Math.max(6,Math.min(tx,cmd2.getBoundingClientRect().left-sr.left-22));
  var sx=c.left-sr.left,sy=c.top+c.height/2-sr.top,r=12,yy=lerp(sy+r,H,q);
  tp.setAttribute('d','M'+sx.toFixed(1)+' '+sy.toFixed(1)+' H '+(tx+r).toFixed(1)+' Q '+tx.toFixed(1)+' '+sy.toFixed(1)+' '+tx.toFixed(1)+' '+(sy+r).toFixed(1)+' V '+yy.toFixed(1));
  tp.style.strokeDashoffset=0;tp.style.opacity=1;
},16.83,.5);
cap(s5.querySelector('.cap-a'),15.3,17.1);
tl.fromTo(s5.querySelector('.lo'),{autoAlpha:0,x:-24},{autoAlpha:1,x:0,duration:.4,ease:'power3.out',immediateRender:false},17.0);
tl.fromTo(s5.querySelector('.le'),{autoAlpha:0,x:24},{autoAlpha:1,x:0,duration:.4,ease:'power3.out',immediateRender:false},17.25);
cap(s5.querySelector('.cap-b'),17.2,17.95);

/* 06 queue + 07 claim */
tl.to(s5,{scale:1.12,autoAlpha:0,duration:.35,ease:'power2.in'},17.95);
tl.set(s6,{autoAlpha:1},18.3);
sceneIn(s6,18.3,{scale:.92});
/* the workers are already polling: cron runs `aits task work --json` every 5 minutes, nothing to claim yet */
$$('.wk .wlog').forEach(function(lg,w){
  var h='';for(var i=1;i<4;i++){var m=35+i*5;h+='<span class="lg"><i>08:'+m+':0'+(w?4:1)+'</i><span class="c-p">$ </span>aits task work <span class="c-f">--json</span></span><span class="lg lo">{"status":"empty"}</span>';}
  lg.innerHTML=h;
});
var qb=$('#qb'),cId=$('#cId'),cNext=$('#cNext'),q0=$('#q0'),q1=$('#q1'),r0=$('#r0'),d1=$('#d1');
function dx(a,b){return function(){return off(b,qb).x-off(a,qb).x;};}
function dy(a,b){return function(){return off(b,qb).y-off(a,qb).y;};}
gsap.set([cId,cNext],{autoAlpha:0});
gsap.set(s6.querySelector('.st-b'),{autoAlpha:0});
gsap.set(s6.querySelector('#wB .wo'),{autoAlpha:0});
var tq1=typer(s6.querySelector('.ql1')),tq2=typer(s6.querySelector('.ql2'));tq1(0);tq2(0);
var twA=typer(s6.querySelector('#wA .wl')),twB=typer(s6.querySelector('#wB .wl'));twA(0);twB(0);
prox(tq1,18.45,.6);
prox(twA,18.5,.55);prox(twB,18.62,.55);
var lease=cId.querySelector('.lease'),woA=s6.querySelector('.wo-a');
gsap.set(lease,{scaleY:0});gsap.set(woA,{autoAlpha:0});
tl.fromTo(cId,{autoAlpha:0,x:dx(r0,q0),y:function(){return dy(r0,q0)()-80;},rotation:-4},{autoAlpha:1,y:dy(r0,q0),rotation:0,duration:.45,ease:'bounce.out',immediateRender:false},19.05);
prox(tq2,19.3,.6);
tl.fromTo(cNext,{autoAlpha:0,x:dx(q0,q1),y:function(){return dy(q0,q1)()-80;},rotation:3},{autoAlpha:1,y:dy(q0,q1),rotation:0,duration:.45,ease:'bounce.out',immediateRender:false},19.95);
cap(s6.querySelector('.cap-a'),18.4,20.55);
tl.to(s6.querySelector('.st-a'),{yPercent:-110,autoAlpha:0,duration:.3},20.55);
tl.fromTo(s6.querySelector('.st-b'),{yPercent:110,autoAlpha:0},{yPercent:0,autoAlpha:1,duration:.35,ease:'power3.out',immediateRender:false},20.65);
/* the claim: a lease line draws from Queued to Running, the card lifts onto a 6px shadow and rides it,
   lands with a small overshoot, and terminal 1 flashes the call that claimed it */
var claimFrom=null,LIFT=70;
var lsv=document.createElementNS(NS,'svg');lsv.setAttribute('class','lease-line');lsv.setAttribute('aria-hidden','true');
var lpath=document.createElementNS(NS,'path');lpath.setAttribute('pathLength','1');lsv.appendChild(lpath);
var lhead=document.createElementNS(NS,'circle');lhead.setAttribute('r','5');lsv.appendChild(lhead);qb.appendChild(lsv);
function leaseD(){
  var a=off(q0,qb),b=off(r0,qb),y0=a.y+q0.offsetHeight*.5,x0=a.x+q0.offsetWidth*.5,x1=b.x+r0.offsetWidth*.5,y1=b.y+r0.offsetHeight*.5;
  return 'M'+x0.toFixed(1)+' '+y0.toFixed(1)+' Q '+((x0+x1)/2).toFixed(1)+' '+(Math.min(y0,y1)-2*LIFT).toFixed(1)+' '+x1.toFixed(1)+' '+y1.toFixed(1);
}
gsap.set(lsv,{autoAlpha:0});
prox(function(p){lpath.setAttribute('d',leaseD());lpath.style.strokeDashoffset=(1-p).toFixed(3);
  var L=lpath.getTotalLength(),pt=lpath.getPointAtLength(L*p);lhead.setAttribute('cx',pt.x);lhead.setAttribute('cy',pt.y);},20.8,.3,'power2.out');
tl.fromTo(lsv,{autoAlpha:0},{autoAlpha:1,duration:.1,immediateRender:false},20.8);
tl.to(lsv,{autoAlpha:0,duration:.3},21.85);
prox(function(p){
  if(!claimFrom||p<.02)claimFrom={x:dx(r0,q0)(),y:dy(r0,q0)()};
  var t=Math.min(1,p/.82),q=ease(t),x=lerp(claimFrom.x,0,q),y=lerp(claimFrom.y,0,q)-Math.sin(Math.PI*t)*LIFT;
  var k=Math.min(1,t*6,(1-t)*6);if(p>.82)k=0;
  if(p>.82){var u=(p-.82)/.18;y=Math.sin(u*Math.PI)*6*(1-u*.3);}
  gsap.set(cId,{x:x,y:y-6*k,scale:1+.04*k,boxShadow:'0 '+(6*k).toFixed(1)+'px 0 rgba(20,22,28,'+(.9*k).toFixed(2)+')'});
},21.05,.65);
function flashLine(el,a,d){prox(function(p){
  if(p<=0||p>=1){el.style.backgroundColor='';el.style.color='';el.style.boxShadow='';return;}
  var k=1-p;el.style.backgroundColor='rgba(255,77,31,'+k.toFixed(2)+')';el.style.boxShadow='0 0 0 3px rgba(255,77,31,'+k.toFixed(2)+')';el.style.color=k>.4?'#14161c':'';
},a,d);}
flashLine(s6.querySelector('#wA .wl'),21.05,.6);
flashLine(woA,23.0,.6);
tl.fromTo(lease,{scaleY:0},{scaleY:1,duration:1.9,ease:'none',immediateRender:false},21.1);
tl.fromTo(woA,{autoAlpha:0,y:8},{autoAlpha:1,y:0,duration:.25,immediateRender:false},23.0);
tl.to(cNext,{x:0,y:0,duration:.5,ease:'power3.inOut'},21.2);
cap(s6.querySelector('.cap-b'),20.75,22.1);
tl.fromTo(s6.querySelector('#wB .wo'),{autoAlpha:0,y:8},{autoAlpha:1,y:0,duration:.25,immediateRender:false},22.2);
cap(s6.querySelector('.cap-c'),22.2,23.4);
tl.to(cId,{x:dx(r0,d1),y:dy(r0,d1),duration:.55,ease:'power3.inOut'},23.0);
tl.to(cNext.querySelector('.lock'),{color:'var(--ok)',duration:.2},23.45);
tl.to(cNext,{x:dx(q0,r0),y:dy(q0,r0),duration:.55,ease:'power3.inOut'},23.6);

/* 08 the line: a dark shutter rises, the two rules slam in */
var k1=$('#k1'),k2=$('#k2'),endi=$('#endi'),endCap=s8.querySelector('.cap');
gsap.set([k1,k2],{whiteSpace:'nowrap',autoAlpha:0});gsap.set([endi,endCap],{autoAlpha:0});
tl.fromTo(shutter,{yPercent:101},{yPercent:0,duration:.55,ease:'power3.inOut',immediateRender:false},24.45);
tl.set(s8,{autoAlpha:1},24.95);
tl.set(s6,{autoAlpha:0},25.05);
tl.fromTo(k1,{'--w':125,xPercent:40,autoAlpha:0},{'--w':66,xPercent:0,autoAlpha:1,duration:1.0,ease:'expo.out',immediateRender:false},24.95);
tl.fromTo(k2,{'--w':125,xPercent:-40,autoAlpha:0},{'--w':66,xPercent:0,autoAlpha:1,duration:1.0,ease:'expo.out',immediateRender:false},25.2);
tl.fromTo(endCap,{autoAlpha:0,y:18},{autoAlpha:1,y:0,duration:.3,immediateRender:false},25.9);
tl.fromTo(endi,{autoAlpha:0,y:20},{autoAlpha:.9,y:0,duration:.5,immediateRender:false},25.7);
tl.to({},{duration:.01},END);
window.aitsFilm=tl;
/* scroll -> film time is not linear: the cuts (type out, the dive and iris, the iris close, the queue push,
   the shutter) get about twice the scroll, the dwell at each rest is cut to under half */
var CUTW=[[2.75,3.5,2],[8.85,10.35,1.9],[14.85,15.5,1.7],[17.95,18.75,1.8],[24.4,25.05,2.4]];
function wAt(t){for(var i=0;i<CUTW.length;i++)if(t>=CUTW[i][0]&&t<CUTW[i][1])return CUTW[i][2];for(var j=0;j<RESTS.length;j++)if(Math.abs(t-RESTS[j])<.3)return .4;return 1;}
var BK=[0,END];CUTW.forEach(function(c){BK.push(c[0],c[1]);});RESTS.forEach(function(r){BK.push(r-.3,r+.3);});
BK=BK.filter(function(t){return t>=0&&t<=END;}).sort(function(a,b){return a-b;}).filter(function(t,i,a){return i===0||t-a[i-1]>1e-6;});
var SEG=[],MEND=0;for(var si=0;si<BK.length-1;si++){var a0=BK[si],b0=BK[si+1],md=(b0-a0)*wAt((a0+b0)/2);SEG.push([a0,b0,MEND,md]);MEND+=md;}
function M(t){for(var i=0;i<SEG.length;i++){var g=SEG[i];if(t<=g[1])return g[2]+(t-g[0])/(g[1]-g[0])*g[3];}return MEND;}
function I(m){for(var i=0;i<SEG.length;i++){var g=SEG[i];if(m<=g[2]+g[3])return g[0]+(m-g[2])/g[3]*(g[1]-g[0]);}return END;}
var cutShare=CUTW.reduce(function(a,c){return a+(M(c[1])-M(c[0]));},0)/MEND;
var film={m:0};tl.time(0);
var master=gsap.timeline({scrollTrigger:{trigger:stage,start:'top top',end:function(){return '+='+Math.round(innerHeight*6);},pin:true,pinSpacing:true,scrub:.6,invalidateOnRefresh:true,anticipatePin:1,pinReparent:false,
  snap:{snapTo:RESTS.map(function(t){return M(t)/MEND;}),duration:{min:.25,max:.8},delay:.08,ease:'power3.out',inertia:false}}});
master.fromTo(film,{m:0},{m:MEND,duration:MEND,ease:'none',immediateRender:false,onUpdate:function(){tl.time(I(film.m));}});
window.aitsMaster=master;window.aitsCutShare=cutShare;
/* deep links and SKIP: the pin must be settled on the first frame after the jump */
function settle(){ScrollTrigger.refresh();ScrollTrigger.update();}
addEventListener('hashchange',function(){requestAnimationFrame(settle);});
document.querySelectorAll('a[href^="#"]').forEach(function(a){a.addEventListener('click',function(){requestAnimationFrame(function(){requestAnimationFrame(settle);});});}); /* exposed for frame-accurate review captures */

/* HUD: scene number, scrub bar, timecode; grid parallax */
var names=['type','spawn','work','assemble','hand off','queue','claim','the line'],cuts=[0,2.9,6.0,9.5,14.3,18.2,20.6,24.45];
var hud=$('.hud'),nN=$('#scnN'),nT=$('#scnT'),fill=$('#tickFill'),tc=$('#tc'),grid=$('.grid-bg'),lastS=-1,lastDark=null,lastLite=null,lastT=-1,lastBg='';
var tickEls=[].slice.call(hud.querySelectorAll('.ticks i'));
/* the scene number is a roller: the old digits roll up and out, the new ones roll in from below */
nN.textContent='';nN.classList.add('roll');var nCur=document.createElement('span');nCur.textContent='01';nN.appendChild(nCur);
var blink=document.createElement('div');blink.className='blink';blink.setAttribute('aria-hidden','true');stage.appendChild(blink);
var tco={v:0},tcq=gsap.quickTo(tco,'v',{duration:.45,ease:'power3.out'}),tcLast=-1;
function sceneChange(s,dir){
  var nx=document.createElement('span');nx.textContent='0'+(s+1);nN.appendChild(nx);
  var old=nCur;nCur=nx;
  gsap.fromTo(nx,{yPercent:dir*100},{yPercent:0,duration:.28,ease:'power3.out'});
  gsap.to(old,{yPercent:-dir*100,duration:.28,ease:'power3.out',onComplete:function(){if(old.parentNode)old.parentNode.removeChild(old);}});
  nT.textContent=names[s];nT.classList.remove('inv');void nT.offsetWidth;nT.classList.add('inv');
  clearTimeout(nT._t);nT._t=setTimeout(function(){nT.classList.remove('inv');},120);
  gsap.fromTo(blink,{opacity:.55},{opacity:0,duration:.07,ease:'none'});
  var tk=tickEls[s];if(tk)gsap.fromTo(tk,{scaleY:2.6,backgroundColor:'#ff4d1f'},{scaleY:1,backgroundColor:'',duration:.45,ease:'power3.out',clearProps:'backgroundColor,transform'});
}
var spacer=stage.parentNode&&stage.parentNode.classList.contains('pin-spacer')?stage.parentNode:null;
function sceneBg(t){
  if(t>=9.6&&t<15.2)return getComputedStyle(iris).backgroundColor;
  if(t>24.75)return getComputedStyle(shutter).backgroundColor;
  if(t>=2.9&&t<=9.75){var b=getComputedStyle(s2).backgroundColor;if(b&&b!=='rgba(0, 0, 0, 0)'&&b!=='transparent')return b;}
  return getComputedStyle(stage).backgroundColor;
}
gsap.ticker.add(function(){
  var t=tl.time();
  if(t!==lastT){
    lastT=t;
    var s=0;for(var j=0;j<cuts.length;j++)if(t>=cuts[j])s=j;
    if(s!==lastS){if(lastS<0){lastS=s;nCur.textContent='0'+(s+1);nT.textContent=names[s];}else{var dir=s>lastS?1:-1;lastS=s;sceneChange(s,dir);}}
    var dark=(t>9.7&&t<15.2)||t>24.75;trail.classList.toggle('mb',t>15.3);if(dark!==lastDark){lastDark=dark;hud.classList.toggle('on-dark',dark);}
    var lite=t>=2.9&&t<=9.75;if(lite!==lastLite){lastLite=lite;hud.classList.toggle('on-light',lite);}
    fill.style.transform='scaleX('+(t/END).toFixed(4)+')';
    grid.style.transform='translate3d('+(-(t*46)%64).toFixed(1)+'px,'+(-(t*23)%64).toFixed(1)+'px,0)';
    tcq(t/END*81.234*24);
    if(!spacer&&stage.parentNode&&stage.parentNode.classList.contains('pin-spacer'))spacer=stage.parentNode;
    var bg=sceneBg(t);if(bg!==lastBg){lastBg=bg;if(spacer)spacer.style.backgroundColor=bg;}
  }
  /* the timecode rolls toward its target instead of stepping */
  var f=Math.round(tco.v);if(f!==tcLast){tcLast=f;var fr=f%24,sec=Math.floor(f/24)%60,min=Math.floor(f/1440);
    tc.textContent='00:'+(min<10?'0':'')+min+':'+(sec<10?'0':'')+sec+':'+(fr<10?'0':'')+fr;}
});

/* ---------- the rest of the page ---------- */
var rv=document.querySelectorAll('main section:not(.hero):not(.film) .sec-h, .grp, .exits>div, .contract>div, .prow a, .band-notes li, .isnot>div, .close .wrap>div, .demo, .syn-foot');
rv.forEach(function(el){el.classList.add('rv');});
var io=new IntersectionObserver(function(es){es.forEach(function(e){if(e.isIntersecting){var el=e.target,d=[].indexOf.call(el.parentNode.children,el)%4*.06;gsap.to(el,{opacity:1,y:0,duration:.55,delay:d,ease:'power3.out'});io.unobserve(el);}});},{rootMargin:'0px 0px 6% 0px'});
rv.forEach(function(el){io.observe(el);});
gsap.utils.toArray('.exits .n').forEach(function(n){
  gsap.fromTo(n,{'--w':62},{'--w':125,ease:'none',scrollTrigger:{trigger:'.exits',start:'top 90%',end:'top 30%',scrub:.5}});
});
gsap.utils.toArray('.gname,.sec-h h2').forEach(function(h){
  h.style.fontVariationSettings='"wdth" var(--w,78)';
  gsap.fromTo(h,{'--w':112},{'--w':74,ease:'none',scrollTrigger:{trigger:h,start:'top 98%',end:'top 55%',scrub:.4}});
});
/* Built to be parsed: with scroll, the stdout line splits and each value flies to its row */
var parse=document.getElementById('parse');
if(parse){
  var pbs=[].slice.call(parse.querySelectorAll('#pLine b')),pgeo=[],pfl=[];
  parse.style.position='relative';
  pbs.forEach(function(b){
    var dd=parse.querySelector('dd[data-k="'+b.getAttribute('data-k')+'"]');
    var f=document.createElement('span');f.className='pfly';f.textContent=b.textContent;f.setAttribute('aria-hidden','true');parse.appendChild(f);
    pfl.push({b:b,dd:dd,f:f,landed:false});
  });
  var pmeasure=function(){var pr=parse.getBoundingClientRect();pgeo=pfl.map(function(o){var a=o.b.getBoundingClientRect(),c=o.dd.getBoundingClientRect();return {ax:a.left-pr.left,ay:a.top-pr.top,cx:c.right-pr.left-o.f.offsetWidth,cy:c.top-pr.top+(c.height-o.f.offsetHeight)/2};});};
  var pdraw=function(P){
    if(!pgeo.length)pmeasure();
    pfl.forEach(function(o,i){
      var g=pgeo[i],p=Math.max(0,Math.min(1,(P-i*.11)/.5)),q=ease(p);
      var x=lerp(g.ax,g.cx,q),y=lerp(g.ay,g.cy,q)-Math.sin(Math.PI*q)*60;
      o.f.style.transform='translate('+x.toFixed(1)+'px,'+y.toFixed(1)+'px) scale('+(1+.3*Math.sin(Math.PI*q)).toFixed(3)+') rotate('+(-4*Math.sin(Math.PI*q)).toFixed(2)+'deg)';
      o.f.style.opacity=p>0&&p<1?1:0;
      o.b.classList.toggle('out',p>0);
      var land=p>=1;o.dd.classList.toggle('wait',!land);
      if(land&&!o.landed){o.dd.classList.remove('hit');void o.dd.offsetWidth;o.dd.classList.add('hit');}
      o.landed=land;
    });
  };
  var pst=ScrollTrigger.create({trigger:parse,start:'top 82%',end:'bottom 38%',scrub:.4,onUpdate:function(t){pdraw(t.progress);},onRefresh:function(t){pmeasure();pdraw(t.progress);}});
  pdraw(pst.progress||0);
}

/* For agents: the skill install runs, then SKILL.md indexes into the job table */
var skill=document.querySelector('.band .install.skill pre'),jrows=[].slice.call(document.querySelectorAll('.band .jobs tbody tr'));
if(skill&&jrows.length){
  var tsk=typer(skill);
  jrows.forEach(function(r){r.classList.add('pre');});
  ScrollTrigger.create({trigger:skill,start:'top 78%',once:true,onEnter:function(){
    tsk(0);skill.parentNode.classList.add('running');
    tween(1100,tsk,function(){
      skill.parentNode.classList.remove('running');
      jrows.forEach(function(r,i){setTimeout(function(){r.classList.remove('pre');r.classList.add('ix');},120+i*95);});
    });
  }});
}

/* one live object for the rest of the page: the session_id chip rides the thread rail and docks into each
   section's demo as the rail passes it (the --session flag, the chosen command, SKILL.md's continue row) */
if(!mobile&&matchMedia('(min-width:761px)').matches&&pageThread){
  var rc=document.createElement('span');rc.className='rail-chip';rc.setAttribute('aria-hidden','true');rc.textContent='session_id';document.body.appendChild(rc);
  var lastHX=0,lastHY=0,curDock=null;
  var docks=[
    function(){var g=[].filter.call(document.querySelectorAll('#commands .lines .g'),function(e){return /--session/.test(e.textContent);})[0];return g;},
    function(){var pn=document.querySelector('#usage .panel:not(.off)');if(!pn)return null;var sid=[].filter.call(pn.querySelectorAll('.turn .sid'),function(e){var w=e.closest('.turn').querySelector('.who');return w&&/you/i.test(w.textContent);})[0];return sid||pn.querySelector('.turn pre');},
    function(){var tr=document.querySelectorAll('#agents .jobs tbody tr')[1];return tr?tr.querySelector('td:last-child code'):null;}
  ];
  var startY=function(){return document.getElementById('commands').getBoundingClientRect().top+scrollY;};
  var endY=function(){var c=document.querySelector('.close');return c.getBoundingClientRect().top+scrollY+80;};
  var sstep=function(t){t=Math.max(0,Math.min(1,t));return t*t*(3-2*t);};
  window.aitsRail=function(hx,hy){
    if(hx==null){hx=lastHX;hy=lastHY;}else{lastHX=hx;lastHY=hy;}
    var s0=startY(),e0=endY(),vis=sstep((hy-s0)/80)*(1-sstep((hy-e0)/80));
    var X=hx,Y=hy,w=0,best=null,rot=-90,sc=1,cw=rc.offsetWidth,ch=rc.offsetHeight;
    for(var i=0;i<docks.length;i++){
      var el=docks[i]();if(!el)continue;var r=el.getBoundingClientRect();if(!r.height)continue;
      var ty=r.top+scrollY+r.height/2,dd=Math.abs(hy-ty),k=1-sstep((dd-70)/120);
      if(k>w){w=k;best={x:r.left+scrollX+Math.min(r.width,cw*(r.height/ch))/2,y:ty,s:Math.max(.8,r.height/ch*1.05),el:el};}
    }
    if(best){var q=w<1?w*w*(3-2*w):1;X=lerp(hx,best.x,q);Y=lerp(hy,best.y,q);rot=lerp(-90,0,q);sc=lerp(1,best.s,q);}
    rc.style.opacity=vis.toFixed(3);
    rc.style.transform='translate('+(X-cw/2).toFixed(1)+'px,'+(Y-ch/2).toFixed(1)+'px) rotate('+rot.toFixed(1)+'deg) scale('+sc.toFixed(3)+')';
    var dk=best&&w>.97?best.el:null;
    if(dk!==curDock){if(curDock)curDock.classList.remove('docked');if(dk){dk.classList.remove('docked');void dk.offsetWidth;dk.classList.add('docked');}curDock=dk;rc.classList.toggle('in',!!dk);}
  };
  document.querySelectorAll('#usage [role="tab"]').forEach(function(t){t.addEventListener('click',function(){aitsRail();});});
  pageThread.draw();
}
ScrollTrigger.addEventListener('refresh',function(){if(window.pageThreadRelayout)window.pageThreadRelayout();});
if(document.fonts&&document.fonts.ready)document.fonts.ready.then(function(){geo=null;ScrollTrigger.refresh();if(!mobile)measure();});
addEventListener('resize',function(){geo=null;});
if(pageThread)ScrollTrigger.addEventListener('refresh',pageThread.layout);
})();
