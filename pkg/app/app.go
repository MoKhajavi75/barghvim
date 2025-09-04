package app

import (
	"net/http"

	"github.com/MoKhajavi75/barghvim/internal/server"
)

func Handler() http.Handler {
	return server.New()
}
