//go:build !embedui

package webassets

import "net/http"

func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Dashboard assets are not embedded in this development binary.", http.StatusNotFound)
	})
}
