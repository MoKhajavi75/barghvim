package handler

import (
	"net/http"

	"github.com/MoKhajavi75/barghvim/pkg/app"
)

var h = app.Handler()

func Handler(w http.ResponseWriter, req *http.Request) {
	h.ServeHTTP(w, req)
}
