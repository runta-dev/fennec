window.installTerminalLinks=(terminal,openLink)=>{
  terminal.registerLinkProvider?.({provideLinks(row,callback){
   const buffer=terminal.buffer.active;let first=row-1;
   while(first>0 && buffer.getLine(first)?.isWrapped && row-first<32)first--;
   let text='',positions=[];
   for(let line=first;line<first+32;line++){
    const value=buffer.getLine(line);if(!value || (line>first&&!value.isWrapped))break;
    for(let x=0;x<terminal.cols;x++){
     const cell=value.getCell(x);if(!cell||cell.getWidth()===0)continue;
     const chars=cell.getChars()||' ';text+=chars;
     for(let i=0;i<chars.length;i++)positions.push({x:x+1,y:line+1});
    }
   }
   const links=[];
   for(const match of text.matchAll(/https:\/\/[^\s<>"\x00-\x1f]+/g)){
    const url=match[0].replace(/[.,;]+$/,'');const start=positions[match.index],end=positions[match.index+url.length-1];
    if(start&&end&&row>=start.y&&row<=end.y)links.push({range:{start,end},text:url,activate(){openLink(url);}});
   }
   callback(links);
  }});
};
