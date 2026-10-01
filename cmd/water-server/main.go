package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/SurTeam/Water/internal/goserver"
)

func defaultSocket() string {
	if p := os.Getenv("WATER_SOCKET"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".water", "dev", "water-go.sock")
}

func main() {
	socket := flag.String("socket", defaultSocket(), "Unix control socket")
	flag.Parse()

	srv := goserver.New(*socket)
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		_ = srv.Close()
	}()

	fmt.Fprintf(os.Stderr, "water-server(go): %s\n", *socket)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
