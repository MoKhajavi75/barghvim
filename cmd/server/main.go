package main

import (
	"log"

	"github.com/MoKhajavi75/barghvim/internal/server"
)

func main() {
	r := server.New()
	if err := r.Run(); err != nil {
		log.Fatal(err)
	}
}
