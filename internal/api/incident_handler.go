package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-038/internal/model"
	"github.com/wyw14/solo-038/internal/service"
)

type incidentAcknowledgeRequest struct {
	Actor    string `json:"actor"`
	Assignee string `json:"assignee"`
}

type incidentAssignRequest struct {
	Actor    string `json:"actor"`
	Assignee string `json:"assignee"`
}

type incidentActionRequest struct {
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Note   string `json:"note"`
}

type incidentResetRequest struct {
	Actor string `json:"actor"`
	Note  string `json:"note"`
}

type incidentResetReviewRequest struct {
	Reviewer string `json:"reviewer"`
	Approve  bool   `json:"approve"`
	Note     string `json:"note"`
}

func (handler *Handler) incidents(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.Incidents())
}

func (handler *Handler) incidentGroups(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.IncidentGroups())
}

func (handler *Handler) acknowledgeIncident(writer http.ResponseWriter, request *http.Request) {
	var input incidentAcknowledgeRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	incident, err := handler.runtime.AcknowledgeIncident(incidentID(request), input.Actor, input.Assignee)
	writeIncidentResult(writer, incident, err)
}

func (handler *Handler) assignIncident(writer http.ResponseWriter, request *http.Request) {
	var input incidentAssignRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	incident, err := handler.runtime.AssignIncident(incidentID(request), input.Actor, input.Assignee)
	writeIncidentResult(writer, incident, err)
}

func (handler *Handler) addIncidentAction(writer http.ResponseWriter, request *http.Request) {
	var input incidentActionRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	incident, err := handler.runtime.AddIncidentAction(incidentID(request), input.Actor, input.Action, input.Note)
	writeIncidentResult(writer, incident, err)
}

func (handler *Handler) requestIncidentReset(writer http.ResponseWriter, request *http.Request) {
	var input incidentResetRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	incident, err := handler.runtime.RequestIncidentReset(incidentID(request), input.Actor, input.Note)
	writeIncidentResult(writer, incident, err)
}

func (handler *Handler) reviewIncidentReset(writer http.ResponseWriter, request *http.Request) {
	var input incidentResetReviewRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	incident, err := handler.runtime.ReviewIncidentReset(incidentID(request), input.Reviewer, input.Approve, input.Note)
	writeIncidentResult(writer, incident, err)
}

func incidentID(request *http.Request) model.EventID {
	return model.EventID(chi.URLParam(request, "id"))
}

func writeIncidentResult(writer http.ResponseWriter, incident service.Incident, err error) {
	if err == nil {
		writeJSON(writer, http.StatusOK, incident)
		return
	}
	if errors.Is(err, service.ErrIncidentNotFound) {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writeError(writer, http.StatusConflict, err)
}
