(function(){
  var root=document.documentElement;

  // theme toggle (initial value is applied by the inline head script)
  var tb=document.getElementById('theme');
  if(tb)tb.addEventListener('click',function(){
    var cur=root.getAttribute('data-theme');
    if(!cur)cur=matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light';
    var next=cur==='dark'?'light':'dark';
    root.setAttribute('data-theme',next);
    tb.setAttribute('aria-label','Switch to '+(next==='dark'?'light':'dark')+' theme');
    try{localStorage.setItem('aits-theme',next);}catch(e){}
  });

  // copy buttons on command examples: copy the "$ " lines without the prompt
  function cmds(pre){return pre.textContent.split('\n').filter(function(l){return l.indexOf('$ ')===0;}).map(function(l){return l.slice(2).replace(/\s+#.*$/,'');}).join('\n');}
  function addCopy(host,pre,label){
    var txt=cmds(pre);if(!txt)return;
    var b=document.createElement('button');b.type='button';b.className='copy cpl';b.textContent='Copy';
    b.setAttribute('data-copy',txt);b.setAttribute('aria-label','Copy '+label);host.appendChild(b);
  }
  document.querySelectorAll('.panel .turn').forEach(function(t){
    var pre=t.querySelector('pre'),who=t.querySelector('.who');
    if(pre&&who&&/^(you|stdin|text)$/i.test(who.textContent.trim()))addCopy(t,pre,'command');
  });
  document.querySelectorAll('.ex-block').forEach(function(x){
    var pre=x.querySelector('pre'),h=x.querySelector('.tx-h');
    if(pre&&h)addCopy(h,pre,'commands: '+h.firstElementChild.textContent);
  });

  // copy buttons
  document.querySelectorAll('.copy').forEach(function(b){
    b.addEventListener('click',function(){
      var txt=b.getAttribute('data-copy');
      function ok(){b.textContent='Copied';b.classList.add('done');setTimeout(function(){b.textContent='Copy';b.classList.remove('done');},1600);}
      function fallback(){var ta=document.createElement('textarea');ta.value=txt;ta.setAttribute('readonly','');ta.style.position='fixed';ta.style.left='-9999px';document.body.appendChild(ta);ta.select();try{document.execCommand('copy');ok();}catch(e){}document.body.removeChild(ta);}
      if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(txt).then(ok,fallback);}else fallback();
    });
  });

  // numbered notes light up their field in the transcript
  document.querySelectorAll('.notes li[data-n]').forEach(function(li){
    var n=li.getAttribute('data-n');
    function on(v){document.querySelectorAll('.hl[data-n="'+n+'"]').forEach(function(el){el.classList.toggle('on',v);});}
    li.addEventListener('mouseenter',function(){on(true)});
    li.addEventListener('mouseleave',function(){on(false)});
    li.addEventListener('focus',function(){on(true)});
    li.addEventListener('blur',function(){on(false)});
  });

  // keep short code tokens such as --agent on one line; long ones may wrap
  document.querySelectorAll('p code, li code, td code, dd code').forEach(function(c){
    var t=c.textContent;if(t.length<=28&&!/\s/.test(t))c.style.whiteSpace='nowrap';
  });

  // let one-line JSON wrap after commas instead of mid-key
  document.querySelectorAll('pre.json, .json pre').forEach(function(pre){
    var w=document.createTreeWalker(pre,NodeFilter.SHOW_TEXT),n,list=[];
    while((n=w.nextNode()))list.push(n);
    list.forEach(function(t){if(t.nodeValue.indexOf(',')>-1)t.nodeValue=t.nodeValue.replace(/,(?!\s)/g,',​');});
  });

  // tabs: each panel sizes to its own content
  document.querySelectorAll('[role="tablist"]').forEach(function(list){
    var tabs=[].slice.call(list.querySelectorAll('[role="tab"]'));
    function sel(t,focus){
      tabs.forEach(function(x){
        var s=x===t,p=document.getElementById(x.getAttribute('aria-controls'));
        x.setAttribute('aria-selected',s);x.tabIndex=s?0:-1;
        p.classList.toggle('off',!s);
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
    var first=tabs.filter(function(t){return t.getAttribute('aria-selected')==='true'})[0]||tabs[0];
    sel(first);
  });

  // table of contents: mark the section in view
  var toc=document.querySelector('.toc');
  if(toc&&'IntersectionObserver' in window){
    var links={},secs=[];
    toc.querySelectorAll('a[href^="#"]').forEach(function(a){
      var s=document.getElementById(a.getAttribute('href').slice(1));
      if(s){links[s.id]=a;secs.push(s);}
    });
    var current=null;
    function mark(id){
      if(id===current)return;current=id;
      Object.keys(links).forEach(function(k){if(k===id)links[k].setAttribute('aria-current','true');else links[k].removeAttribute('aria-current');});
      var a=links[id],ol=a&&a.parentNode.parentNode;
      if(a&&ol&&ol.scrollWidth>ol.clientWidth){ol.scrollLeft=a.offsetLeft-ol.clientWidth/2+a.offsetWidth/2;}
    }
    // the active section is the last one whose top has reached the line under the bar and TOC
    var line=function(){return matchMedia('(max-width:980px)').matches?130:120;};
    function spy(){
      var y=line(),pick=secs[0];
      for(var i=0;i<secs.length;i++){if(secs[i].getBoundingClientRect().top<=y)pick=secs[i];else break;}
      if(pick)mark(pick.id);
    }
    var queued=false;
    function onScroll(){if(queued)return;queued=true;requestAnimationFrame(function(){queued=false;spy();});}
    var io=new IntersectionObserver(onScroll,{rootMargin:'-80px 0px -60% 0px'});
    secs.forEach(function(s){io.observe(s)});
    addEventListener('scroll',onScroll,{passive:true});
    addEventListener('resize',onScroll);
    spy();
  }

  // install boxes: fade the right edge while there is more to scroll
  document.querySelectorAll('.install pre, .ex-block pre').forEach(function(pre){
    function upd(){pre.classList.toggle('fade',pre.scrollWidth-pre.clientWidth-pre.scrollLeft>2);}
    pre.addEventListener('scroll',upd,{passive:true});
    addEventListener('resize',upd);
    upd();
    if(document.fonts&&document.fonts.ready)document.fonts.ready.then(upd);
  });

  // hero transcript: finished at rest, animated on Replay
  var tx=document.getElementById('herotx');
  if(tx){
    var turns=[].slice.call(tx.querySelectorAll('.turn'));
    var from=tx.querySelector('[data-link="from"]'),to=tx.querySelector('[data-link="to"]');
    var btn=tx.querySelector('.replay'),flag=tx.querySelector('.c-f.hl[data-n="2"]');
    function mark(v){[from,to,flag].forEach(function(e){if(e)e.classList.toggle('linked',v);});}
    var NS='http://www.w3.org/2000/svg',svg=document.createElementNS(NS,'svg'),path=document.createElementNS(NS,'path'),c1=document.createElementNS(NS,'circle'),c2=document.createElementNS(NS,'circle');
    svg.setAttribute('class','tx-link');svg.setAttribute('aria-hidden','true');
    c1.setAttribute('r','3');c2.setAttribute('r','3');
    svg.appendChild(path);svg.appendChild(c1);svg.appendChild(c2);tx.appendChild(svg);
    function link(){
      if(!from||!to)return;
      var b=tx.getBoundingClientRect(),f=from.getBoundingClientRect(),t=to.getBoundingClientRect();
      svg.setAttribute('width',b.width);svg.setAttribute('height',b.height);
      // route around the text: from the first session id (past its pin), out to the right, down, and into the id passed to --session
      var rp=from.closest('pre'),rr=document.createRange();rr.selectNodeContents(rp);
      var right=0;[].forEach.call(rr.getClientRects(),function(r){if(r.right>right)right=r.right;});
      var pin=from.nextElementSibling&&from.nextElementSibling.classList.contains('pin')?from.nextElementSibling.getBoundingClientRect():f;
      var x1=pin.right-b.left+3,y1=f.top-b.top+f.height/2,x2=t.right-b.left+3,y2=t.top-b.top+t.height/2;
      var xr=Math.min(Math.max(right,t.right)-b.left+14,b.width-7);
      var d='M'+x1+' '+y1+' H'+xr+' V'+y2+' H'+x2;
      path.setAttribute('d',d);
      c1.setAttribute('cx',x1);c1.setAttribute('cy',y1);c2.setAttribute('cx',x2);c2.setAttribute('cy',y2);
      var len=Math.abs(xr-x1)+Math.abs(y2-y1)+Math.abs(xr-x2);svg.style.setProperty('--len',Math.ceil(len));
    }
    var timers=[];
    function clear(){timers.forEach(clearTimeout);timers=[];}
    function finish(){
      clear();tx.classList.remove('play');
      turns.forEach(function(t){t.classList.remove('wait','typing');});
      mark(true);
      link();svg.style.display='';
    }
    var still=matchMedia('(prefers-reduced-motion: reduce)');
    function play(){
      if(still.matches){finish();return;}
      clear();
      mark(false);
      svg.style.display='none';svg.classList.remove('draw');
      tx.classList.add('play');
      turns.forEach(function(t){t.classList.add('wait');t.classList.remove('typing');});
      var at=[150,650,1150,1700,2000];
      turns.forEach(function(t,i){
        timers.push(setTimeout(function(){t.classList.remove('wait');if(!t.classList.contains('reply')&&!t.classList.contains('err'))t.classList.add('typing');},at[i]||at[at.length-1]));
      });
      timers.push(setTimeout(function(){
        tx.classList.remove('play');turns.forEach(function(t){t.classList.remove('typing');});
        link();svg.style.display='';void svg.getBoundingClientRect();svg.classList.add('draw');
        mark(true);
      },2450));
    }
    if(btn){btn.hidden=false;btn.addEventListener('click',play);}
    addEventListener('resize',function(){if(svg.style.display!=='none')link();});
    if(document.fonts&&document.fonts.ready)document.fonts.ready.then(function(){if(svg.style.display!=='none')link();});
    // show the finished exchange at rest; Replay animates it
    finish();
  }
})();
