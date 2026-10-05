package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	port := flag.Int("port", 8554, "TCP port to listen on")
	file := flag.String("file", "assets/demo.ulaw", "8kHz mono PCMU (u-law) media file")
	flag.Parse()

	srv, err := newServer(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := srv.listenAndServe(fmt.Sprintf(":%d", *port)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
