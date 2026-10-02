package main

import (
	"fmt"
	"os"

	"github.com/SurTeam/Water/internal/gouiapp"
)

func main() {
	args:=os.Args[1:]
	var err error
	if len(args)>0 && isControlArgument(args[0]) {
		err=run(args)
	} else {
		err=gouiapp.Run(args,buildVariant)
	}
	if err!=nil{
		fmt.Fprintln(os.Stderr,"water:",err)
		os.Exit(1)
	}
}

func isControlArgument(first string) bool {
	switch first {
	case "ctl","state","info","version","--version","-V","client",
		"connections","connection","sockets","socket","ui","server","debug",
		"workspace","tab","pane","surface","terminal","operation","scenario","ping":
		return true
	default:
		return false
	}
}
