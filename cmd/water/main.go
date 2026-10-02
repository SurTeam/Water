package main

import (
	"fmt"
	"os"
)

func main() {
	if err:=run(os.Args[1:]);err!=nil{
		fmt.Fprintln(os.Stderr,"water:",err)
		os.Exit(1)
	}
}
