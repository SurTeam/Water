'use strict';
// Runs only in the browser; image pixels and decoded invitations are not uploaded.
importScripts('/jsqr.js');
// Gaussian 3x3 suppresses screen-pixel/moire texture while retaining QR edges.
// Separable [1,2,1] passes stay within the same bounded browser worker.
function smooth(pixels,width,height){
 const horizontal=new Uint16Array(width*height*3);
 for(let y=0;y<height;y++)for(let x=0;x<width;x++){
  const left=Math.max(0,x-1),right=Math.min(width-1,x+1),index=(y*width+x)*3;
  for(let c=0;c<3;c++)horizontal[index+c]=pixels[(y*width+left)*4+c]+2*pixels[(y*width+x)*4+c]+pixels[(y*width+right)*4+c];
 }
 for(let y=0;y<height;y++)for(let x=0;x<width;x++){
  const above=Math.max(0,y-1),below=Math.min(height-1,y+1),index=(y*width+x)*4;
  for(let c=0;c<3;c++)pixels[index+c]=(horizontal[(above*width+x)*3+c]+2*horizontal[(y*width+x)*3+c]+horizontal[(below*width+x)*3+c]+8)>>4;
 }
 return pixels;
}
self.onmessage=event=>{
 try{
  const {pixels,width,height}=event.data;
  if(!Number.isInteger(width)||!Number.isInteger(height)||width<1||height<1||width*height>5760000||pixels.byteLength!==width*height*4)throw new Error('Invalid QR image size');
  const rgba=new Uint8ClampedArray(pixels);
  let result=jsQR(rgba,width,height,{inversionAttempts:'attemptBoth'});
  if(!result)result=jsQR(smooth(rgba,width,height),width,height,{inversionAttempts:'attemptBoth'});
  self.postMessage({text:result?.data||''});
 }catch(error){self.postMessage({error:error instanceof Error?error.message:String(error)});}
};
