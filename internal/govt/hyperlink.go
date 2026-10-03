package govt

import (
	"bytes"
	"strings"
)

const maxOSC8Bytes = 64 * 1024

type osc8Tracker struct {
	parser osc8Parser
	nextID int
	activeID int
	entriesWithID map[string]int
	keyByID map[int]string
	uriByID map[int]string
}

func newOSC8Tracker()*osc8Tracker{
	return &osc8Tracker{
		nextID:1,
		entriesWithID:make(map[string]int),
		keyByID:make(map[int]string),
		uriByID:make(map[int]string),
	}
}

func (t *osc8Tracker) feed(data []byte) {
	for _, event := range t.parser.feed(data) {
		if event.uri == "" {
			if strings.TrimSpace(event.params) == "" {
				t.activeID = 0
			}
			continue
		}
		if t.activeID != 0 {
			t.activeID = 0
		}
		idParam := ""
		for _, param := range strings.Split(event.params, ":") {
			if strings.HasPrefix(param, "id=") {
				idParam = strings.TrimPrefix(param, "id=")
				break
			}
		}
		if idParam != "" {
			key := idParam + ";;" + event.uri
			if id, ok := t.entriesWithID[key]; ok {
				t.activeID = id
				continue
			}
			id := t.allocate(event.uri)
			t.entriesWithID[key] = id
			t.keyByID[id] = key
			t.activeID = id
			continue
		}
		t.activeID = t.allocate(event.uri)
	}
}

func (t *osc8Tracker) allocate(uri string)int{
	id:=t.nextID
	t.nextID++
	t.uriByID[id]=uri
	return id
}

func (t *osc8Tracker) uri(id int)string{
	if id==0{return ""}
	return t.uriByID[id]
}

func (t *osc8Tracker) prune(live map[int]struct{}){
	if t.activeID!=0{live[t.activeID]=struct{}{}}
	for id:=range t.uriByID{
		if _,ok:=live[id];ok{continue}
		delete(t.uriByID,id)
		if key:=t.keyByID[id];key!=""{
			delete(t.keyByID,id)
			delete(t.entriesWithID,key)
		}
	}
}

type osc8Event struct{params,uri string}

type osc8Parser struct{
	state uint8
	buf []byte
}

const(
	osc8Normal uint8=iota
	osc8Escape
	osc8Body
	osc8BodyEscape
)

func (p *osc8Parser) feed(data []byte)[]osc8Event{
	if len(data)>0 && (p.state==osc8Normal || p.state==osc8Escape) &&
		!(p.state==osc8Escape && data[0]==']') &&
		bytes.IndexByte(data,0x9d)<0 && !bytes.Contains(data,[]byte("\x1b]")) {
		// An ordinary CSI/SGR chunk cannot open an OSC. Keep a trailing ESC
		// pending, but avoid a state-machine dispatch for every text byte.
		p.state=osc8Normal
		if data[len(data)-1]==0x1b{p.state=osc8Escape}
		return nil
	}
	var out []osc8Event
	for _,b:=range data{
		switch p.state{
		case osc8Normal:
			switch b{
			case 0x1b:p.state=osc8Escape
			case 0x9d:p.startBody()
			}
		case osc8Escape:
			if b==']'{
				p.startBody()
			}else if b==0x1b{
				p.state=osc8Escape
			}else{
				p.state=osc8Normal
			}
		case osc8Body:
			switch b{
			case 0x07,0x9c:
				if ev,ok:=p.finish();ok{out=append(out,ev)}
			case 0x1b:
				p.state=osc8BodyEscape
			default:
				p.append(b)
			}
		case osc8BodyEscape:
			if b=='\\'{
				if ev,ok:=p.finish();ok{out=append(out,ev)}
			}else{
				p.append(0x1b)
				p.append(b)
				if p.state!=osc8Normal{p.state=osc8Body}
			}
		}
	}
	return out
}

func (p *osc8Parser) startBody(){
	p.state=osc8Body
	p.buf=p.buf[:0]
}

func (p *osc8Parser) append(b byte){
	if len(p.buf)>=maxOSC8Bytes{
		p.buf=p.buf[:0]
		p.state=osc8Normal
		return
	}
	p.buf=append(p.buf,b)
}

func (p *osc8Parser) finish()(osc8Event,bool){
	data:=string(p.buf)
	p.buf=p.buf[:0]
	p.state=osc8Normal
	if !strings.HasPrefix(data,"8;"){return osc8Event{},false}
	rest:=strings.TrimPrefix(data,"8;")
	idx:=strings.IndexByte(rest,';')
	if idx<0{return osc8Event{},false}
	return osc8Event{params:rest[:idx],uri:rest[idx+1:]},true
}
