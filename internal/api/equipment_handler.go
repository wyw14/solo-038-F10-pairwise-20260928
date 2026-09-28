package api

import "net/http"

func (handler *Handler) equipment(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.Equipment())
}

func (handler *Handler) interlocks(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.Interlocks())
}
