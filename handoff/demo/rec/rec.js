const puppeteer=require('puppeteer-core');const fs=require('fs');const {spawn}=require('child_process');
const S='/workspace/console-places-review/shots', H='/home/brenden';
const THEME=process.env.THEME||'catppuccin', FS=+(process.env.FS||22), OUT=process.env.OUT||'/workspace/demo/raw.mp4';
const START=process.env.START||H+'/projects';
const script=JSON.parse(fs.readFileSync(process.env.SCRIPT||__dirname+'/script.json','utf8'));
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
(async()=>{
 const toml=fs.readFileSync(`${S}/themes/${THEME}/colors.toml`,'utf8');const c={};
 for(const l of toml.split('\n')){const m=l.match(/^(\w+)\s*=\s*"(#[0-9a-fA-F]{6})"/);if(m)c[m[1]]=m[2];}
 const theme={background:c.background,foreground:c.foreground,cursor:c.foreground,
  black:c.color0||c.dark_background||'#000000',red:c.red,green:c.green,yellow:c.yellow,blue:c.blue,magenta:c.magenta,cyan:c.cyan,white:c.foreground,
  brightBlack:c.muted||c.dark_foreground,brightRed:c.bright_red||c.red,brightGreen:c.bright_green||c.green,brightYellow:c.bright_yellow||c.yellow,brightBlue:c.bright_blue||c.blue,brightMagenta:c.bright_magenta||c.magenta,brightCyan:c.bright_cyan||c.cyan,brightWhite:c.bright_foreground||c.foreground};
 const bgf=fs.readdirSync(`${S}/themes/${THEME}`).find(f=>f.startsWith('bg.'));
 const b=await puppeteer.launch({executablePath:'/usr/bin/google-chrome',headless:false,defaultViewport:null,
   ignoreDefaultArgs:['--enable-automation'],
   args:['--no-sandbox','--allow-file-access-from-files','--force-device-scale-factor=1','--kiosk','--window-position=0,0','--window-size=1920,1080','--hide-scrollbars','--disable-infobars','--no-first-run','--disable-gpu-vsync','--test-type']});
 const p=(await b.pages())[0]||await b.newPage();
 let py;
 await p.exposeFunction('toPty',d=>{ if(py) py.stdin.write(d); });
 await p.goto('file://'+__dirname+'/page.html');
 const dim=await p.evaluate(o=>window.setup(o),{bg:`file://${S}/themes/${THEME}/${bgf}`,theme,fontSize:FS,alpha:+(process.env.ALPHA||0.9)});
 console.log(JSON.stringify(dim));
 const env={HOME:H,PATH:'/usr/bin:/bin',USER:'brenden',TERM:'xterm-256color',COLORTERM:'truecolor',LANG:'C.UTF-8',
   XDG_STATE_HOME:H+'/.local/state',XDG_CONFIG_HOME:H+'/.config',CONSOLE_PLACES_IMAGES:'sixel'};
 py=spawn('python3',[__dirname+'/bridge.py',dim.cols,dim.rows,Math.round(dim.cols*dim.cw),Math.round(dim.rows*dim.ch),'--',__dirname+'/jail.sh',START],{env,cwd:START});
 let q=Promise.resolve();
 py.stdout.on('data',d=>{const s=d.toString('base64');q=q.then(()=>p.evaluate(x=>window.feed(x),s)).catch(e=>console.error(e));});
 py.stderr.on('data',d=>process.stderr.write(d));
 await sleep(+(process.env.WARM||3000));
 let ff;
 if(OUT!=='none'){
  ff=spawn('ffmpeg',['-y','-loglevel','error','-f','x11grab','-framerate','30','-video_size','1920x1080','-draw_mouse','0','-i',process.env.DISPLAY+'.0','-c:v','libx264','-preset','ultrafast','-qp','0','-pix_fmt','yuv444p',OUT],{stdio:['pipe','inherit','inherit']});
  await sleep(700);
 }
 const t0=Date.now();
 const K={down:'\x1b[B',up:'\x1b[A',right:'\x1b[C',left:'\x1b[D',enter:'\r',esc:'\x1b'};
 for(const st of script){
   if(st.wait){await sleep(st.wait);continue;}
   if(st.shot){await p.screenshot({path:st.shot});continue;}
   const n=st.n||1;
   for(let i=0;i<n;i++){ py.stdin.write(K[st.key]??st.key); await sleep((st.gap||220)+Math.round(Math.random()*(st.jit??60))); }
 }
 console.log('script ms',Date.now()-t0);
 if(ff){ ff.stdin.write('q'); await new Promise(r=>ff.on('close',r)); }
 py.stdin.write('q'); await sleep(300); py.kill(); await b.close(); process.exit(0);
})();
