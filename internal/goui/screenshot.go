package goui

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget/material"
)

func (c *WorkspaceClient) Screenshot(path string)(map[string]any,error){
	path=strings.TrimSpace(path)
	if path==""{path="target/water-screenshot.png"}
	if filepath.Ext(path)==""{path+=".png"}
	if parent:=filepath.Dir(path);parent!=""&&parent!="."{
		if err:=os.MkdirAll(parent,0o755);err!=nil{return nil,fmt.Errorf("could not create screenshot directory: %w",err)}
	}

	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()

	size:=c.frameSize
	metric:=c.frameMetric
	if size.X<=0||size.Y<=0{
		size=image.Pt(int(c.config.Startup.WindowWidth),int(c.config.Startup.WindowHeight))
	}
	if size.X<1{size.X=1};if size.Y<1{size.Y=1}

	var ops op.Ops
	gtx:=layout.Context{
		Constraints:layout.Exact(size),
		Metric:metric,
		Now:time.Now(),
		Ops:&ops,
	}
	th:=material.NewTheme()
	c.layoutUnlocked(gtx,th)

	window,err:=headless.NewWindow(size.X,size.Y)
	if err!=nil{return nil,fmt.Errorf("could not create headless Gio window: %w",err)}
	defer window.Release()
	if err:=window.Frame(&ops);err!=nil{return nil,fmt.Errorf("could not render Water screenshot: %w",err)}

	img:=image.NewRGBA(image.Rect(0,0,size.X,size.Y))
	if err:=window.Screenshot(img);err!=nil{return nil,fmt.Errorf("could not read Water screenshot: %w",err)}
	file,err:=os.Create(path)
	if err!=nil{return nil,fmt.Errorf("could not create screenshot %s: %w",path,err)}
	if err:=png.Encode(file,img);err!=nil{
		_ = file.Close()
		return nil,fmt.Errorf("could not encode screenshot %s: %w",path,err)
	}
	if err:=file.Close();err!=nil{return nil,fmt.Errorf("could not close screenshot %s: %w",path,err)}

	return map[string]any{
		"path":path,
		"width":size.X,
		"height":size.Y,
	},nil
}
