/* aits: shared motion for every page (header, thread, magnetic buttons, reading fuse) */
(function(){
'use strict';
var R=document.documentElement;
var reduce=matchMedia('(prefers-reduced-motion: reduce)').matches;
var finePointer=matchMedia('(hover:hover) and (pointer:fine)').matches;

/* header: hides while reading down, returns on the way up (reference pages only) */
var bar=document.querySelector('.bar');
var fuse=document.querySelector('.fuse');
var cssFuse=window.CSS&&CSS.supports&&CSS.supports('animation-timeline','scroll()');
if(bar){
  var lastY=scrollY,q=false;
  addEventListener('scroll',function(){
    if(q)return;q=true;
    requestAnimationFrame(function(){
      q=false;var y=scrollY,docPage=document.body.classList.contains('doc-page');
      if(docPage&&!reduce){if(y>lastY+6&&y>240)bar.classList.add('hid');else if(y<lastY-6)bar.classList.remove('hid');}
      lastY=y;
      if(fuse&&!cssFuse){var H=document.documentElement.scrollHeight-innerHeight;fuse.style.setProperty('--p',H>0?(y/H).toFixed(4):0);}
    });
  },{passive:true});
  bar.addEventListener('focusin',function(){bar.classList.remove('hid');});
}

/* magnetic buttons: they lean toward the pointer */
if(finePointer&&!reduce){
  document.querySelectorAll('.btn,.gh,.copy:not(.cpl),.key').forEach(function(b){
    b.classList.add('mag');
    b.addEventListener('pointermove',function(e){
      var r=b.getBoundingClientRect(),dx=(e.clientX-r.left-r.width/2)/r.width,dy=(e.clientY-r.top-r.height/2)/r.height;
      b.style.transform='translate('+(dx*10).toFixed(1)+'px,'+(dy*8).toFixed(1)+'px)';
    });
    b.addEventListener('pointerleave',function(){b.style.transform='';});
  });
}

/* thread: one vermilion line from a start point down the page, lighting a node at each section */
window.aitsThread=function(opts){
  var NS='http://www.w3.org/2000/svg';
  var svg=document.createElementNS(NS,'svg');svg.setAttribute('class','thread');svg.setAttribute('aria-hidden','true');
  var ghost=document.createElementNS(NS,'path');ghost.setAttribute('class','ghost');
  var path=document.createElementNS(NS,'path');
  var head=document.createElementNS(NS,'circle');head.setAttribute('class','head');head.setAttribute('r','4.5');
  svg.appendChild(ghost);svg.appendChild(path);
  var dots=[];
  document.body.appendChild(svg);
  var L=0,map=[],nodes=[],ready=false,lastP=-1,caret=null;
  function docPos(el){var r=el.getBoundingClientRect();return {x:r.left+scrollX,y:r.top+scrollY,w:r.width,h:r.height};}
  function layout(){
    var start=opts.start();if(!start){svg.style.display='none';return;}
    svg.style.display='';
    var H=document.documentElement.scrollHeight,W=document.documentElement.clientWidth;
    svg.setAttribute('width',W);svg.setAttribute('height',H);svg.style.height=H+'px';
    var s=docPos(start),sx=s.x+s.w/2,sy=s.y+s.h;
    if(opts.fromLeft){sx=opts.x();sy=s.y+s.h-2;}
    var X=opts.x(),endY=opts.end?opts.end():H-40,tail='',endEl=opts.endEl&&opts.endEl();
    caret=endEl||null;
    if(endEl){
      /* end in the closing caret: along the gutter, under the heading, up into the block */
      var e=docPos(endEl),blk=docPos(endEl.closest('h1,h2,h3,p')||endEl),cx=e.x+e.w/2,cb=e.y+e.h,yb=blk.y+blk.h+Math.max(12,e.h*.42),r=14;
      endY=yb;
      tail=' Q '+X.toFixed(1)+' '+yb.toFixed(1)+' '+(X+r).toFixed(1)+' '+yb.toFixed(1)+' L '+(cx-r).toFixed(1)+' '+yb.toFixed(1)+' Q '+cx.toFixed(1)+' '+yb.toFixed(1)+' '+cx.toFixed(1)+' '+(yb-r).toFixed(1)+' L '+cx.toFixed(1)+' '+(cb+1).toFixed(1);
    }
    var els=opts.nodes();
    var drop=opts.fromLeft?0:opts.drop||Math.min(220,Math.max(120,Math.abs(sx-X)*.35));
    var by=opts.bend?Math.max(sy,opts.bend()):sy;
    var d='M'+sx.toFixed(1)+' '+sy.toFixed(1)+' L '+sx.toFixed(1)+' '+by.toFixed(1)+' C '+sx.toFixed(1)+' '+(by+drop*.8).toFixed(1)+', '+X.toFixed(1)+' '+(by+drop*.4).toFixed(1)+', '+X.toFixed(1)+' '+(by+drop*1.4).toFixed(1)+' L '+X.toFixed(1)+' '+(tail?endY-14:endY).toFixed(1)+tail;
    sy=by;
    path.setAttribute('d',d);ghost.setAttribute('d',d);
    L=path.getTotalLength();
    path.style.strokeDasharray=L+' '+L;
    /* y -> length lookup */
    map=[];var N=tail?160:60,lyb=-1;for(var i=0;i<=N;i++){var l=L*i/N,p=path.getPointAtLength(l),vy=p.y;
      /* past the turn under the heading, y stops growing: keep drawing as the reader scrolls on */
      if(tail){if(lyb<0&&p.y>=endY-.5)lyb=l;if(lyb>=0)vy=endY+(l-lyb)*.22;}
      map.push([vy,l]);}
    dots.forEach(function(c){svg.removeChild(c);});dots=[];nodes=[];
    els.forEach(function(el){
      var p=docPos(el),y=p.y+Math.min(p.h/2,14);if(y<sy+drop*1.4||y>endY)return;
      var c=document.createElementNS(NS,'circle');c.setAttribute('cx',X);c.setAttribute('cy',y);c.setAttribute('r',6);
      svg.appendChild(c);dots.push(c);nodes.push(y);
    });
    svg.appendChild(head);
    ready=true;lastP=-1;draw();
  }
  function lenAt(y){
    if(y<=map[0][0])return 0;
    for(var i=1;i<map.length;i++){if(map[i][0]>=y){var a=map[i-1],b=map[i],t=(y-a[0])/Math.max(1e-3,b[0]-a[0]);return a[1]+(b[1]-a[1])*t;}}
    return L;
  }
  function draw(){
    if(!ready)return;
    var y=reduce?1e9:scrollY+innerHeight*(opts.at||.62),l=lenAt(y);
    if(Math.abs(l-lastP)<.5)return;lastP=l;
    path.style.strokeDashoffset=(L-l).toFixed(1);
    var p=path.getPointAtLength(Math.max(0,l));head.setAttribute('cx',p.x.toFixed(1));head.setAttribute('cy',p.y.toFixed(1));
    head.style.opacity=l>2&&l<L-2?1:0;
    for(var i=0;i<nodes.length;i++)dots[i].classList.toggle('on',nodes[i]<=y);
    if(caret)caret.classList.toggle('lit',l>=L-2);
    if(opts.onDraw)opts.onDraw(p.x,p.y);
  }
  var q=false;
  addEventListener('scroll',function(){if(q)return;q=true;requestAnimationFrame(function(){q=false;draw();});},{passive:true});
  var t=0;function relayout(){clearTimeout(t);t=setTimeout(layout,120);}
  addEventListener('resize',relayout);
  if('ResizeObserver' in window)new ResizeObserver(relayout).observe(document.body);
  if(document.fonts&&document.fonts.ready)document.fonts.ready.then(relayout);
  layout();
  return {layout:relayout,draw:draw};
};

/* reference pages: thread from the heading's square through every command or principle */
if(document.body.classList.contains('doc-page')){
  var gx=function(){var w=document.querySelector('.doc')||document.querySelector('.wrap');var r=w.getBoundingClientRect(),pl=parseFloat(getComputedStyle(w).paddingLeft)||16;return r.left+scrollX+Math.max(7,pl*.45);};
  aitsThread({
    start:function(){return document.querySelector('.doc-head');},fromLeft:true,
    x:gx,
    nodes:function(){return [].slice.call(document.querySelectorAll('.cmd-h h2,.prin .big'));},
    end:function(){var f=document.querySelector('footer');return f?f.getBoundingClientRect().top+scrollY-24:document.documentElement.scrollHeight-60;}
  });
}

/* ---------- reference pages: the film's world, carried over ---------- */
function typerOf(el){
  var nodes=[],w=document.createTreeWalker(el,NodeFilter.SHOW_TEXT),n;
  while((n=w.nextNode()))nodes.push({n:n,t:n.nodeValue});
  var total=nodes.reduce(function(a,b){return a+b.t.length;},0),last=-1;
  var f=function(p){var k=Math.round(Math.max(0,Math.min(1,p))*total);if(k===last)return;last=k;
    for(var i=0;i<nodes.length;i++){var o=nodes[i],m=Math.min(k,o.t.length),v=o.t.slice(0,m);if(o.n.nodeValue!==v)o.n.nodeValue=v;k-=m;}};
  f.total=total;return f;
}
function play(ms,fn,done){var t0=0;function f(t){if(!t0)t0=t;var p=Math.min(1,(t-t0)/ms);fn(p);if(p<1)requestAnimationFrame(f);else if(done)done();}requestAnimationFrame(f);}
function onEnter(els,fn,margin){
  if(!('IntersectionObserver' in window))return;
  var io=new IntersectionObserver(function(es){es.forEach(function(e){if(e.isIntersecting){io.unobserve(e.target);fn(e.target);}});},{rootMargin:margin||'0px 0px -18% 0px'});
  els.forEach(function(el){io.observe(el);});
}
var below=function(el){return el.getBoundingClientRect().top>innerHeight*.8;};
if(document.body.classList.contains('doc-page')&&!reduce){
  /* CLI reference: each usage bar is a live terminal. It types `aits help COMMAND`, prints the usage line,
     then lists the flags; each flags-table row lights up as its flag is typed */
  var usages=[].slice.call(document.querySelectorAll('pre.usage')).filter(below);
  var flagTable=function(pre){var el=pre.nextElementSibling;while(el&&!el.matches('section,.cmd-h,pre.usage')){if(el.matches('table.tbl')&&/^flag$/i.test((el.querySelector('th')||{}).textContent||''))return el;if(el.matches('h3')&&!/^flags$/i.test(el.textContent.trim()))return null;el=el.nextElementSibling;}return null;};
  onEnter(usages,function(pre){
    var k=pre.querySelector('.k'),cmd=k?k.textContent.replace(/^aits\s*/,''):'';
    var tbl=flagTable(pre),frows=tbl?[].slice.call(tbl.querySelectorAll('tbody tr')):[];
    var ty=typerOf(pre);ty(0);pre.classList.add('typing','live');
    var h=null,th=null;
    if(cmd!=='help'){h=document.createElement('span');h.className='uh';h.setAttribute('aria-hidden','true');
      h.innerHTML='<span class="c-p">$ </span>aits help'+(cmd?' '+cmd:'');pre.insertBefore(h,pre.firstChild);th=typerOf(h);th(0);}
    var flags=function(){
      if(!frows.length){pre.classList.remove('typing');return;}
      var f=document.createElement('span');f.className='uf';f.setAttribute('aria-hidden','true');pre.appendChild(f);
      var names=frows.map(function(r){return r.cells[0].textContent.trim().split(/\s+/)[0];}),i=0;
      f.innerHTML='Flags:';
      var one=function(){
        if(i>=names.length){pre.classList.remove('typing');frows.forEach(function(r){r.classList.remove('lit');});return;}
        var sp=document.createElement('span');sp.className='uff';sp.textContent=' '+names[i];f.appendChild(sp);var t=typerOf(sp);t(0);
        var row=frows[i];
        play(Math.max(120,names[i].length*28),t,function(){frows.forEach(function(r){r.classList.remove('lit');});row.classList.add('lit','seen');i++;setTimeout(one,70);});
      };
      one();
    };
    var body=function(){play(Math.min(520,ty.total/90*1000),ty,flags);};
    if(th)play(th.total/50*1000,th,body);else body();
  },'0px 0px -22% 0px');
  /* principles: the rule's title types out once, at 60 characters a second */
  var heads=[].slice.call(document.querySelectorAll('.prin h2')).filter(below);
  onEnter(heads,function(h){
    var ty=typerOf(h);ty(0);h.classList.add('typing');
    play(ty.total/60*1000,ty,function(){h.classList.remove('typing');});
  },'0px 0px -30% 0px');
  /* principles: each numeral is a guard. A check mark draws, scrubbed by scroll, as its rule passes the middle */
  var bigs=[].slice.call(document.querySelectorAll('.prin .big'));
  if(bigs.length){
    var NSs='http://www.w3.org/2000/svg';
    var marks=bigs.map(function(b){
      var sv=document.createElementNS(NSs,'svg');sv.setAttribute('class','guard');sv.setAttribute('viewBox','0 0 48 48');sv.setAttribute('aria-hidden','true');
      sv.innerHTML='<circle class="gr" cx="24" cy="24" r="21" pathLength="1"/><path class="gk" d="M13 25 L21 33 L36 15" pathLength="1"/>';
      b.appendChild(sv);return {b:b,a:b.closest('.prin'),c:sv.querySelector('.gr'),k:sv.querySelector('.gk'),last:-1};
    });
    var gq=false,gdraw=function(){gq=false;var H=innerHeight;
      marks.forEach(function(m){var r=m.a.getBoundingClientRect(),p=Math.max(0,Math.min(1,(H*.82-r.top)/(H*.36)));if(Math.abs(p-m.last)<.004)return;m.last=p;
        var pr=Math.min(1,p/.55),pk=Math.max(0,(p-.5)/.5);
        m.c.style.strokeDashoffset=(1-pr).toFixed(3);m.k.style.strokeDashoffset=(1-pk).toFixed(3);
        m.b.classList.toggle('pass',p>=1);});};
    addEventListener('scroll',function(){if(!gq){gq=true;requestAnimationFrame(gdraw);}},{passive:true});addEventListener('resize',gdraw);gdraw();
  }
  /* the overview grid runs like CI: each tile queues, runs, then passes, one after another */
  /* principles hero: the fifteen tiles run like a CI matrix on load: queued, running (spinner and a sweep),
     then pass (check), 40ms apart, while the counter ticks up to 15/15 */
  var tiles=[].slice.call(document.querySelectorAll('.pgrid li'));
  if(tiles.length){
    var cc=document.querySelector('.cicount'),ccN=cc&&cc.querySelector('.cc-n'),done=0;
    tiles.forEach(function(li){var a=li.querySelector('a');var st=document.createElement('span');st.className='ci';st.setAttribute('aria-hidden','true');a.appendChild(st);li.classList.add('ci-q');});
    if(cc){cc.classList.add('run');ccN.textContent='00';}
    var runCI=function(){tiles.forEach(function(li,i){setTimeout(function(){li.classList.remove('ci-q');li.classList.add('ci-run');setTimeout(function(){li.classList.remove('ci-run');li.classList.add('ci-ok');done++;
      if(ccN){ccN.textContent=(done<10?'0':'')+done;ccN.classList.remove('tick');void ccN.offsetWidth;ccN.classList.add('tick');if(done===tiles.length){cc.classList.remove('run');cc.classList.add('ok');}}},420);},180+i*40);});};
    var pg=document.querySelector('.pgrid');
    if(pg.getBoundingClientRect().top<innerHeight)runCI();
    else if('IntersectionObserver' in window){var cio=new IntersectionObserver(function(es){if(es[0].isIntersecting){cio.disconnect();runCI();}},{threshold:.3});cio.observe(pg);}else runCI();
  }
  /* CLI reference hero: `aits help` types at the prompt, the title prints behind a block cursor,
     then the TOC lights up entry by entry like a streaming --help tree (about 600ms) */
  var dh=document.querySelector('.doc-head[data-hero="help"]');
  if(dh){
    var hp=dh.querySelector('.hp'),h1=dh.querySelector('h1'),tn=h1.firstChild,chs=[];
    if(tn&&tn.nodeType===3){var fr=document.createDocumentFragment();tn.nodeValue.split('').forEach(function(c){var sp=document.createElement('span');sp.className='hc';sp.textContent=c;fr.appendChild(sp);chs.push(sp);});h1.replaceChild(fr,tn);}
    var cur=document.createElement('span');cur.className='hcur';cur.setAttribute('aria-hidden','true');
    var links=[].slice.call(document.querySelectorAll('.toc ol li'));
    dh.classList.add('hero-run');links.forEach(function(l){l.classList.add('tq');});
    var thp=typerOf(hp);thp(0);
    setTimeout(function(){play(260,thp,function(){
      hp.classList.add('ran');
      var i=0;var step=function(){if(i<chs.length){chs[i].classList.add('on');chs[i].parentNode.insertBefore(cur,chs[i].nextSibling);i++;setTimeout(step,24);}else{if(cur.parentNode)cur.parentNode.removeChild(cur);dh.classList.remove('hero-run');
        links.forEach(function(l,j){setTimeout(function(){l.classList.remove('tq');l.classList.add('tlit');setTimeout(function(){l.classList.remove('tlit');},380);},j*Math.round(600/links.length));});}};
      step();
    });},150);
  }
  /* TOC: the "$" marker glides between items (FLIP) instead of jumping */
  var tol=document.querySelector('.toc ol');
  if(tol&&matchMedia('(min-width:981px)').matches){
    var mk=document.createElement('span');mk.className='toc-mk';mk.setAttribute('aria-hidden','true');mk.textContent='$';
    tol.appendChild(mk);document.querySelector('.toc').classList.add('has-mk');
    var place=function(){var a=tol.querySelector('a[aria-current="true"]');if(!a){mk.style.opacity=0;return;}
      var first=mk.getBoundingClientRect().top;mk.style.transition='none';mk.style.transform='translateY('+a.offsetTop+'px)';mk.style.opacity=1;
      var dy=first-mk.getBoundingClientRect().top;if(!dy)return;
      mk.style.transform='translateY('+(a.offsetTop+dy)+'px)';void mk.offsetWidth;
      mk.style.transition='';mk.style.transform='translateY('+a.offsetTop+'px)';};
    new MutationObserver(place).observe(tol,{subtree:true,attributes:true,attributeFilter:['aria-current']});
    place();
  }
}
})();
