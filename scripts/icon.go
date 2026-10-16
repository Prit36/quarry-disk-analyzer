//go:build ignore

// Run go run scripts/icon.go to regenerate the procedural application icon.
package main

import("image";"image/color";"image/png";"os")
func main(){
	im:=image.NewRGBA(image.Rect(0,0,1024,1024));background:=color.RGBA{17,20,24,255};accent:=color.RGBA{195,238,137,255};dark:=color.RGBA{26,38,21,255}
	for y:=0;y<1024;y++{for x:=0;x<1024;x++{im.SetRGBA(x,y,background)}}
	box:=func(x,y,w,h,r int,c color.RGBA){for py:=y;py<y+h;py++{for px:=x;px<x+w;px++{dx,dy:=0,0;if px<x+r{dx=x+r-px}else if px>=x+w-r{dx=px-(x+w-r-1)};if py<y+r{dy=y+r-py}else if py>=y+h-r{dy=py-(y+h-r-1)};if dx*dx+dy*dy<=r*r{im.SetRGBA(px,py,c)}}}}
	box(96,96,832,832,196,accent);box(290,285,224,454,30,dark);box(576,285,158,254,25,dark);box(576,600,158,139,25,dark)
	os.MkdirAll("resources",0755);f,err:=os.Create("resources/icon.png");if err!=nil{panic(err)};defer f.Close();if err:=png.Encode(f,im);err!=nil{panic(err)}
}
