package service

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/solo-038/internal/model"
)

// Incident lifecycle statuses.
const (
	IncidentStatusOpen         = "open"
	IncidentStatusAcknowledged = "acknowledged"
	IncidentStatusResetPending = "reset_pending"
	IncidentStatusReset        = "reset"
)

// SeverityCritical marks faults that keep blocking their related operations
// until they have been reset through the reset review flow.
const SeverityCritical = "critical"

var ErrIncidentNotFound = errors.New("incident not found")

type IncidentAction struct {
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at"`
}

type ResetReview struct {
	RequestedBy string     `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	ReviewedBy  string     `json:"reviewed_by,omitempty"`
	ReviewedAt  *time.Time `json:"reviewed_at,omitempty"`
	Approved    bool       `json:"approved"`
}

type Incident struct {
	ID             model.EventID    `json:"id"`
	Severity       string           `json:"severity"`
	Component      string           `json:"component"`
	Message        string           `json:"message"`
	At             time.Time        `json:"at"`
	Status         string           `json:"status"`
	Assignee       string           `json:"assignee,omitempty"`
	AcknowledgedBy string           `json:"acknowledged_by,omitempty"`
	AcknowledgedAt *time.Time       `json:"acknowledged_at,omitempty"`
	Interlocks     []Interlock      `json:"interlocks"`
	Actions        []IncidentAction `json:"actions"`
	ResetReview    *ResetReview     `json:"reset_review,omitempty"`
}

// IncidentGroup merges repeated alarms from the same source for display.
// The underlying incidents are never removed; this is only a presentation view.
type IncidentGroup struct {
	Severity    string          `json:"severity"`
	Component   string          `json:"component"`
	Message     string          `json:"message"`
	Count       int             `json:"count"`
	OpenCount   int             `json:"open_count"`
	FirstAt     time.Time       `json:"first_at"`
	LastAt      time.Time       `json:"last_at"`
	IncidentIDs []model.EventID `json:"incident_ids"`
}

// operationComponents maps an operation kind to the components whose unreset
// critical incidents block that operation.
var operationComponents = map[string][]string{
	"load_roll":        {"unwind"},
	"feed_web":         {"unwind", "web"},
	"stop_press":       {"press"},
	"adjust_phase":     {"press", "register"},
	"switch_ink":       {"ink"},
	"start_dryer":      {"dryer"},
	"release_interval": {"register", "dryer"},
	"finish_output":    {"folder"},
	"start_splice":     {"splice", "unwind"},
}

var incidentMu sync.Mutex

func (runtime *Runtime) RecordIncident(severity, component, message string) Incident {
	// Capture the interlock snapshot before taking the incident lock so the
	// snapshot reflects the machine state at the moment the fault was raised.
	snapshot := runtime.Interlocks()
	now := time.Now().UTC()
	incidentMu.Lock()
	defer incidentMu.Unlock()
	incident := Incident{
		ID:         model.NewEventID(),
		Severity:   severity,
		Component:  component,
		Message:    message,
		At:         now,
		Status:     IncidentStatusOpen,
		Interlocks: snapshot,
		Actions:    []IncidentAction{{Actor: "system", Action: "reported", Note: message, At: now}},
	}
	runtime.incidents = append(runtime.incidents, incident)
	return incident
}

func (runtime *Runtime) Incidents() []Incident {
	incidentMu.Lock()
	defer incidentMu.Unlock()
	values := append([]Incident(nil), runtime.incidents...)
	sort.Slice(values, func(left, right int) bool { return values[left].At.After(values[right].At) })
	return values
}

// IncidentGroups merges repeated alarms with the same severity, component and
// message into a single row for display. Raw incidents stay intact and remain
// available through Incidents.
func (runtime *Runtime) IncidentGroups() []IncidentGroup {
	incidentMu.Lock()
	defer incidentMu.Unlock()
	type groupKey struct {
		severity  string
		component string
		message   string
	}
	groups := map[groupKey]*IncidentGroup{}
	order := []groupKey{}
	for _, incident := range runtime.incidents {
		key := groupKey{severity: incident.Severity, component: incident.Component, message: incident.Message}
		group, ok := groups[key]
		if !ok {
			group = &IncidentGroup{Severity: incident.Severity, Component: incident.Component, Message: incident.Message, FirstAt: incident.At, LastAt: incident.At}
			groups[key] = group
			order = append(order, key)
		}
		group.Count++
		if incident.Status != IncidentStatusReset {
			group.OpenCount++
		}
		if incident.At.Before(group.FirstAt) {
			group.FirstAt = incident.At
		}
		if incident.At.After(group.LastAt) {
			group.LastAt = incident.At
		}
		group.IncidentIDs = append(group.IncidentIDs, incident.ID)
	}
	values := make([]IncidentGroup, 0, len(order))
	for _, key := range order {
		values = append(values, *groups[key])
	}
	sort.Slice(values, func(left, right int) bool { return values[left].LastAt.After(values[right].LastAt) })
	return values
}

// BlockingIncidents returns the unreset critical incidents that block the
// given operation kind.
func (runtime *Runtime) BlockingIncidents(kind string) []Incident {
	components, ok := operationComponents[kind]
	if !ok || len(components) == 0 {
		return nil
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	var blocked []Incident
	for _, incident := range runtime.incidents {
		if incident.Severity != SeverityCritical || incident.Status == IncidentStatusReset {
			continue
		}
		for _, component := range components {
			if incident.Component == component {
				blocked = append(blocked, incident)
				break
			}
		}
	}
	return blocked
}

func (runtime *Runtime) findIncident(id model.EventID) (int, error) {
	for index, incident := range runtime.incidents {
		if incident.ID == id {
			return index, nil
		}
	}
	return -1, ErrIncidentNotFound
}

// AcknowledgeIncident records who took over the incident, optionally assigning
// a responsible person at the same time.
func (runtime *Runtime) AcknowledgeIncident(id model.EventID, actor, assignee string) (Incident, error) {
	if actor == "" {
		return Incident{}, fmt.Errorf("actor is required")
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	index, err := runtime.findIncident(id)
	if err != nil {
		return Incident{}, err
	}
	incident := &runtime.incidents[index]
	if incident.Status != IncidentStatusOpen {
		return Incident{}, fmt.Errorf("incident %s is %s and cannot be acknowledged", incident.ID, incident.Status)
	}
	now := time.Now().UTC()
	incident.Status = IncidentStatusAcknowledged
	incident.AcknowledgedBy = actor
	incident.AcknowledgedAt = &now
	if assignee != "" {
		incident.Assignee = assignee
	}
	incident.Actions = append(incident.Actions, IncidentAction{Actor: actor, Action: "acknowledged", Note: assignee, At: now})
	return *incident, nil
}

// AssignIncident sets the responsible person for an incident.
func (runtime *Runtime) AssignIncident(id model.EventID, actor, assignee string) (Incident, error) {
	if actor == "" || assignee == "" {
		return Incident{}, fmt.Errorf("actor and assignee are required")
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	index, err := runtime.findIncident(id)
	if err != nil {
		return Incident{}, err
	}
	incident := &runtime.incidents[index]
	if incident.Status == IncidentStatusReset {
		return Incident{}, fmt.Errorf("incident %s is already reset and cannot be reassigned", incident.ID)
	}
	incident.Assignee = assignee
	incident.Actions = append(incident.Actions, IncidentAction{Actor: actor, Action: "assigned", Note: assignee, At: time.Now().UTC()})
	return *incident, nil
}

// AddIncidentAction appends a disposal action to the incident's action log.
func (runtime *Runtime) AddIncidentAction(id model.EventID, actor, action, note string) (Incident, error) {
	if actor == "" || action == "" {
		return Incident{}, fmt.Errorf("actor and action are required")
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	index, err := runtime.findIncident(id)
	if err != nil {
		return Incident{}, err
	}
	incident := &runtime.incidents[index]
	if incident.Status == IncidentStatusReset {
		return Incident{}, fmt.Errorf("incident %s is already reset and cannot accept new actions", incident.ID)
	}
	incident.Actions = append(incident.Actions, IncidentAction{Actor: actor, Action: action, Note: note, At: time.Now().UTC()})
	return *incident, nil
}

// RequestIncidentReset asks for the incident to be reset; the reset only takes
// effect after a reset review approves it.
func (runtime *Runtime) RequestIncidentReset(id model.EventID, actor, note string) (Incident, error) {
	if actor == "" {
		return Incident{}, fmt.Errorf("actor is required")
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	index, err := runtime.findIncident(id)
	if err != nil {
		return Incident{}, err
	}
	incident := &runtime.incidents[index]
	if incident.Status != IncidentStatusOpen && incident.Status != IncidentStatusAcknowledged {
		return Incident{}, fmt.Errorf("incident %s is %s and cannot request reset", incident.ID, incident.Status)
	}
	now := time.Now().UTC()
	incident.Status = IncidentStatusResetPending
	incident.ResetReview = &ResetReview{RequestedBy: actor, RequestedAt: now}
	incident.Actions = append(incident.Actions, IncidentAction{Actor: actor, Action: "reset_requested", Note: note, At: now})
	return *incident, nil
}

// ReviewIncidentReset approves or rejects a pending reset request. Critical
// incidents require a reviewer other than the requester.
func (runtime *Runtime) ReviewIncidentReset(id model.EventID, reviewer string, approve bool, note string) (Incident, error) {
	if reviewer == "" {
		return Incident{}, fmt.Errorf("reviewer is required")
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	index, err := runtime.findIncident(id)
	if err != nil {
		return Incident{}, err
	}
	incident := &runtime.incidents[index]
	if incident.Status != IncidentStatusResetPending || incident.ResetReview == nil {
		return Incident{}, fmt.Errorf("incident %s has no pending reset request", incident.ID)
	}
	if approve && incident.Severity == SeverityCritical && reviewer == incident.ResetReview.RequestedBy {
		return Incident{}, fmt.Errorf("critical incident reset must be reviewed by someone other than the requester")
	}
	now := time.Now().UTC()
	incident.ResetReview.ReviewedBy = reviewer
	incident.ResetReview.ReviewedAt = &now
	incident.ResetReview.Approved = approve
	if approve {
		incident.Status = IncidentStatusReset
		incident.Actions = append(incident.Actions, IncidentAction{Actor: reviewer, Action: "reset_approved", Note: note, At: now})
	} else {
		incident.Status = IncidentStatusAcknowledged
		incident.Actions = append(incident.Actions, IncidentAction{Actor: reviewer, Action: "reset_rejected", Note: note, At: now})
	}
	return *incident, nil
}
