package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type acknowledgeRequest struct {
	Operator string `json:"operator"`
}

type assignRequest struct {
	Assignee string `json:"assignee"`
	Operator string `json:"operator"`
}

type actionRequest struct {
	Operator string `json:"operator"`
	Action   string `json:"action"`
	Note     string `json:"note"`
}

type resetRequest struct {
	Operator string `json:"operator"`
}

type resetReviewRequest struct {
	Reviewer string `json:"reviewer"`
	Approved bool   `json:"approved"`
	Note     string `json:"note"`
}

// incidents lists every incident with its raw occurrences. "?view=groups"
// folds same-source incidents into one row each for display; the underlying
// incidents and occurrences are never merged away.
func (handler *Handler) incidents(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Query().Get("view") == "groups" {
		writeJSON(writer, http.StatusOK, handler.runtime.IncidentGroups())
		return
	}
	writeJSON(writer, http.StatusOK, handler.runtime.Incidents())
}

func (handler *Handler) incident(writer http.ResponseWriter, request *http.Request) {
	incident, ok := handler.runtime.Incident(chi.URLParam(request, "id"))
	if !ok {
		writeJSON(writer, http.StatusNotFound, errorResponse{Error: "incident not found"})
		return
	}
	writeJSON(writer, http.StatusOK, incident)
}

func (handler *Handler) acknowledgeIncident(writer http.ResponseWriter, request *http.Request) {
	var input acknowledgeRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Operator == "" {
		writeJSON(writer, http.StatusBadRequest, errorResponse{Error: "operator is required"})
		return
	}
	handler.mutateIncident(writer, request, func(id string) (any, error) {
		return handler.runtime.Acknowledge(id, input.Operator)
	})
}

func (handler *Handler) assignIncident(writer http.ResponseWriter, request *http.Request) {
	var input assignRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Assignee == "" {
		writeJSON(writer, http.StatusBadRequest, errorResponse{Error: "assignee is required"})
		return
	}
	handler.mutateIncident(writer, request, func(id string) (any, error) {
		return handler.runtime.AssignIncident(id, input.Assignee, input.Operator)
	})
}

func (handler *Handler) addIncidentAction(writer http.ResponseWriter, request *http.Request) {
	var input actionRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Operator == "" || input.Action == "" {
		writeJSON(writer, http.StatusBadRequest, errorResponse{Error: "operator and action are required"})
		return
	}
	handler.mutateIncident(writer, request, func(id string) (any, error) {
		return handler.runtime.AddIncidentAction(id, input.Operator, input.Action, input.Note)
	})
}

func (handler *Handler) requestIncidentReset(writer http.ResponseWriter, request *http.Request) {
	var input resetRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Operator == "" {
		writeJSON(writer, http.StatusBadRequest, errorResponse{Error: "operator is required"})
		return
	}
	handler.mutateIncident(writer, request, func(id string) (any, error) {
		return handler.runtime.RequestReset(id, input.Operator)
	})
}

func (handler *Handler) reviewIncidentReset(writer http.ResponseWriter, request *http.Request) {
	var input resetReviewRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Reviewer == "" {
		writeJSON(writer, http.StatusBadRequest, errorResponse{Error: "reviewer is required"})
		return
	}
	handler.mutateIncident(writer, request, func(id string) (any, error) {
		return handler.runtime.ReviewReset(id, input.Reviewer, input.Approved, input.Note)
	})
}

func (handler *Handler) mutateIncident(writer http.ResponseWriter, request *http.Request, mutate func(string) (any, error)) {
	id := chi.URLParam(request, "id")
	if _, ok := handler.runtime.Incident(id); !ok {
		writeJSON(writer, http.StatusNotFound, errorResponse{Error: "incident not found"})
		return
	}
	incident, err := mutate(id)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusOK, incident)
}
