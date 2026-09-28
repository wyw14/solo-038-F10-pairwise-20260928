package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wyw14/solo-038/internal/model"
)

const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

const (
	IncidentStatusOpen         = "open"
	IncidentStatusAcknowledged = "acknowledged"
	IncidentStatusResetPending = "reset_pending"
	IncidentStatusResolved     = "resolved"
)

// IncidentOccurrence is one raw alarm event. Same-source repeats are merged
// into a single incident for presentation, but every occurrence is preserved
// here and never deleted.
type IncidentOccurrence struct {
	ID      model.EventID `json:"id"`
	At      time.Time     `json:"at"`
	Message string        `json:"message"`
}

// IncidentAction is one disposal step recorded against an incident.
type IncidentAction struct {
	By     string    `json:"by"`
	Action string    `json:"action"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at"`
}

// ResetReview is the return-to-service audit: someone other than the
// requester must approve before a critical fault stops blocking operations.
type ResetReview struct {
	RequestedBy string     `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	ReviewedBy  string     `json:"reviewed_by,omitempty"`
	ReviewedAt  *time.Time `json:"reviewed_at,omitempty"`
	Approved    bool       `json:"approved"`
	Note        string     `json:"note,omitempty"`
}

type Incident struct {
	ID             string               `json:"id"`
	Severity       string               `json:"severity"`
	Component      string               `json:"component"`
	Message        string               `json:"message"`
	At             time.Time            `json:"at"`
	Status         string               `json:"status"`
	Assignee       string               `json:"assignee,omitempty"`
	AcknowledgedBy string               `json:"acknowledged_by,omitempty"`
	AcknowledgedAt *time.Time           `json:"acknowledged_at,omitempty"`
	Actions        []IncidentAction     `json:"actions,omitempty"`
	Reset          *ResetReview         `json:"reset,omitempty"`
	Interlocks     []Interlock          `json:"interlocks"`
	Occurrences    []IncidentOccurrence `json:"occurrences"`
}

// IncidentGroup is the merged presentation of same-source incidents: raw
// occurrences stay on the incidents themselves, this only folds the list.
type IncidentGroup struct {
	Key         string    `json:"key"`
	Component   string    `json:"component"`
	Message     string    `json:"message"`
	Severity    string    `json:"severity"`
	Count       int       `json:"count"`
	OpenCount   int       `json:"open_count"`
	FirstAt     time.Time `json:"first_at"`
	LastAt      time.Time `json:"last_at"`
	IncidentIDs []string  `json:"incident_ids"`
}

func (incident Incident) Source() string {
	return incidentKey(incident.Component, incident.Message)
}

func (incident Incident) OccurrenceCount() int {
	return len(incident.Occurrences)
}

func (incident Incident) LastAt() time.Time {
	if len(incident.Occurrences) == 0 {
		return incident.At
	}
	return incident.Occurrences[len(incident.Occurrences)-1].At
}

func (incident Incident) Resolved() bool {
	return incident.Status == IncidentStatusResolved
}

// Blocking reports whether this incident currently inhibits operations: only
// critical faults block, and only until the reset review approves the return
// to service.
func (incident Incident) Blocking() bool {
	return incident.Severity == SeverityCritical && !incident.Resolved()
}

func incidentKey(component, message string) string {
	return strings.ToLower(strings.TrimSpace(component)) + "|" + strings.ToLower(strings.TrimSpace(message))
}

var incidentMu sync.Mutex

func validSeverity(severity string) bool {
	switch severity {
	case SeverityCritical, SeverityWarning, SeverityInfo:
		return true
	}
	return false
}

// RecordIncident appends a raw occurrence. While an incident from the same
// source is still unresolved the occurrence merges into it; once that
// incident has been resolved a new episode is opened. The interlock snapshot
// is captured at record time so the event keeps the interlock state of its
// moment.
func (runtime *Runtime) RecordIncident(severity, component, message string) (Incident, error) {
	if !validSeverity(severity) {
		return Incident{}, fmt.Errorf("incident severity %q is invalid", severity)
	}
	component = strings.TrimSpace(component)
	message = strings.TrimSpace(message)
	if component == "" || message == "" {
		return Incident{}, fmt.Errorf("incident component and message are required")
	}
	now := time.Now().UTC()
	occurrence := IncidentOccurrence{ID: model.NewEventID(), At: now, Message: message}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	for index := len(runtime.incidents) - 1; index >= 0; index-- {
		incident := &runtime.incidents[index]
		if incident.Resolved() || incident.Source() != incidentKey(component, message) {
			continue
		}
		incident.Occurrences = append(incident.Occurrences, occurrence)
		if severityRank(severity) > severityRank(incident.Severity) {
			incident.Severity = severity
		}
		if err := runtime.persistIncidentsLocked(); err != nil {
			return Incident{}, err
		}
		return *incident, nil
	}
	incident := Incident{
		ID:          string(model.NewEventID()),
		Severity:    severity,
		Component:   component,
		Message:     message,
		At:          now,
		Status:      IncidentStatusOpen,
		Interlocks:  runtime.interlocksLocked(now),
		Occurrences: []IncidentOccurrence{occurrence},
	}
	runtime.incidents = append(runtime.incidents, incident)
	if err := runtime.persistIncidentsLocked(); err != nil {
		return Incident{}, err
	}
	return incident, nil
}

func severityRank(severity string) int {
	switch severity {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	}
	return 0
}

func (runtime *Runtime) Incidents() []Incident {
	incidentMu.Lock()
	defer incidentMu.Unlock()
	values := append([]Incident(nil), runtime.incidents...)
	sort.Slice(values, func(left, right int) bool { return values[left].LastAt().After(values[right].LastAt()) })
	return values
}

// IncidentGroups folds incidents by source for display. Incidents and their
// raw occurrences are untouched; this is purely a merged view.
func (runtime *Runtime) IncidentGroups() []IncidentGroup {
	incidentMu.Lock()
	defer incidentMu.Unlock()
	groups := map[string]*IncidentGroup{}
	order := []string{}
	for _, incident := range runtime.incidents {
		key := incident.Source()
		group, ok := groups[key]
		if !ok {
			group = &IncidentGroup{Key: key, Component: incident.Component, Message: incident.Message, Severity: incident.Severity, FirstAt: incident.At}
			groups[key] = group
			order = append(order, key)
		}
		group.Count += incident.OccurrenceCount()
		if !incident.Resolved() {
			group.OpenCount++
		}
		if severityRank(incident.Severity) > severityRank(group.Severity) {
			group.Severity = incident.Severity
		}
		if incident.At.Before(group.FirstAt) {
			group.FirstAt = incident.At
		}
		if last := incident.LastAt(); last.After(group.LastAt) {
			group.LastAt = last
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

func (runtime *Runtime) Incident(id string) (Incident, bool) {
	incidentMu.Lock()
	defer incidentMu.Unlock()
	for _, incident := range runtime.incidents {
		if incident.ID == id {
			return incident, true
		}
	}
	return Incident{}, false
}

func (runtime *Runtime) findIncidentLocked(id string) (*Incident, error) {
	for index := range runtime.incidents {
		if runtime.incidents[index].ID == id {
			return &runtime.incidents[index], nil
		}
	}
	return nil, fmt.Errorf("incident %q not found", id)
}

// Acknowledge marks who has taken over the incident.
func (runtime *Runtime) Acknowledge(id, operator string) (Incident, error) {
	operator = strings.TrimSpace(operator)
	if operator == "" {
		return Incident{}, fmt.Errorf("operator is required")
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	incident, err := runtime.findIncidentLocked(id)
	if err != nil {
		return Incident{}, err
	}
	if incident.Resolved() {
		return Incident{}, fmt.Errorf("incident %q is already resolved", id)
	}
	if incident.Status != IncidentStatusOpen {
		return Incident{}, fmt.Errorf("incident %q is already acknowledged", id)
	}
	now := time.Now().UTC()
	incident.Status = IncidentStatusAcknowledged
	incident.AcknowledgedBy = operator
	incident.AcknowledgedAt = &now
	if err := runtime.persistIncidentsLocked(); err != nil {
		return Incident{}, err
	}
	return *incident, nil
}

// AssignIncident sets the responsible person. Re-assignment is allowed while
// the incident is unresolved and is recorded as a disposal action.
func (runtime *Runtime) AssignIncident(id, assignee, operator string) (Incident, error) {
	assignee = strings.TrimSpace(assignee)
	operator = strings.TrimSpace(operator)
	if assignee == "" {
		return Incident{}, fmt.Errorf("assignee is required")
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	incident, err := runtime.findIncidentLocked(id)
	if err != nil {
		return Incident{}, err
	}
	if incident.Resolved() {
		return Incident{}, fmt.Errorf("incident %q is already resolved", id)
	}
	incident.Assignee = assignee
	note := "assigned to " + assignee
	if operator != "" {
		note += " by " + operator
	}
	incident.Actions = append(incident.Actions, IncidentAction{By: operator, Action: "assign", Note: note, At: time.Now().UTC()})
	if err := runtime.persistIncidentsLocked(); err != nil {
		return Incident{}, err
	}
	return *incident, nil
}

// AddIncidentAction records a disposal action against the incident.
func (runtime *Runtime) AddIncidentAction(id, operator, action, note string) (Incident, error) {
	operator = strings.TrimSpace(operator)
	action = strings.TrimSpace(action)
	note = strings.TrimSpace(note)
	if operator == "" || action == "" {
		return Incident{}, fmt.Errorf("operator and action are required")
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	incident, err := runtime.findIncidentLocked(id)
	if err != nil {
		return Incident{}, err
	}
	if incident.Resolved() {
		return Incident{}, fmt.Errorf("incident %q is already resolved", id)
	}
	incident.Actions = append(incident.Actions, IncidentAction{By: operator, Action: action, Note: note, At: time.Now().UTC()})
	if err := runtime.persistIncidentsLocked(); err != nil {
		return Incident{}, err
	}
	return *incident, nil
}

// RequestReset asks for return-to-service. The incident must be taken over
// (acknowledged, with a responsible person) and have at least one disposal
// action before a reset can be requested.
func (runtime *Runtime) RequestReset(id, operator string) (Incident, error) {
	operator = strings.TrimSpace(operator)
	if operator == "" {
		return Incident{}, fmt.Errorf("operator is required")
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	incident, err := runtime.findIncidentLocked(id)
	if err != nil {
		return Incident{}, err
	}
	if incident.Resolved() {
		return Incident{}, fmt.Errorf("incident %q is already resolved", id)
	}
	if incident.Status == IncidentStatusResetPending {
		return Incident{}, fmt.Errorf("incident %q already has a pending reset review", id)
	}
	if incident.AcknowledgedBy == "" || incident.Assignee == "" {
		return Incident{}, fmt.Errorf("incident %q must be acknowledged and assigned before reset", id)
	}
	if len(incident.Actions) == 0 {
		return Incident{}, fmt.Errorf("incident %q needs at least one disposal action before reset", id)
	}
	incident.Status = IncidentStatusResetPending
	incident.Reset = &ResetReview{RequestedBy: operator, RequestedAt: time.Now().UTC()}
	if err := runtime.persistIncidentsLocked(); err != nil {
		return Incident{}, err
	}
	return *incident, nil
}

// ReviewReset approves or rejects a pending reset. The reviewer must differ
// from the requester; only an approved review resolves the incident and lifts
// the operation block.
func (runtime *Runtime) ReviewReset(id, reviewer string, approved bool, note string) (Incident, error) {
	reviewer = strings.TrimSpace(reviewer)
	note = strings.TrimSpace(note)
	if reviewer == "" {
		return Incident{}, fmt.Errorf("reviewer is required")
	}
	incidentMu.Lock()
	defer incidentMu.Unlock()
	incident, err := runtime.findIncidentLocked(id)
	if err != nil {
		return Incident{}, err
	}
	if incident.Resolved() {
		return Incident{}, fmt.Errorf("incident %q is already resolved", id)
	}
	if incident.Status != IncidentStatusResetPending || incident.Reset == nil {
		return Incident{}, fmt.Errorf("incident %q has no pending reset review", id)
	}
	if reviewer == incident.Reset.RequestedBy {
		return Incident{}, fmt.Errorf("reset reviewer must differ from the requester")
	}
	now := time.Now().UTC()
	incident.Reset.ReviewedBy = reviewer
	incident.Reset.ReviewedAt = &now
	incident.Reset.Approved = approved
	incident.Reset.Note = note
	if approved {
		incident.Status = IncidentStatusResolved
	} else {
		incident.Status = IncidentStatusAcknowledged
		incident.Actions = append(incident.Actions, IncidentAction{By: reviewer, Action: "reset_rejected", Note: note, At: now})
	}
	if err := runtime.persistIncidentsLocked(); err != nil {
		return Incident{}, err
	}
	return *incident, nil
}

// BlockingIncident returns the unresolved critical incident that blocks the
// given operation kind, if any.
func (runtime *Runtime) BlockingIncident(kind string) (Incident, bool) {
	incidentMu.Lock()
	defer incidentMu.Unlock()
	for _, incident := range runtime.incidents {
		if incident.Blocking() && incidentBlocksOperation(incident.Component, kind) {
			return incident, true
		}
	}
	return Incident{}, false
}

// BlockingIncidents lists every unresolved critical incident, newest first.
func (runtime *Runtime) BlockingIncidents() []Incident {
	incidentMu.Lock()
	defer incidentMu.Unlock()
	return runtime.blockingIncidentsLocked()
}

func (runtime *Runtime) blockingIncidentsLocked() []Incident {
	values := []Incident{}
	for _, incident := range runtime.incidents {
		if incident.Blocking() {
			values = append(values, incident)
		}
	}
	sort.Slice(values, func(left, right int) bool { return values[left].LastAt().After(values[right].LastAt()) })
	return values
}

// operationComponents maps an operation kind to the components whose critical
// faults block it. Fault injection, recovery, and diagnostics stay available
// so an unresolved fault can always be reproduced, investigated, and cleared.
var operationComponents = map[string][]string{
	"load_roll":        {"unwind", "web"},
	"feed_web":         {"unwind", "web"},
	"stop_press":       {"press"},
	"adjust_phase":     {"register", "press"},
	"switch_ink":       {"ink"},
	"start_dryer":      {"dryer"},
	"release_interval": {"register", "dryer"},
	"finish_output":    {"folder"},
	"start_splice":     {"splice", "press", "web"},
}

func incidentBlocksOperation(component, kind string) bool {
	components, ok := operationComponents[kind]
	if !ok {
		return false
	}
	for _, candidate := range components {
		if candidate == "*" || candidate == component {
			return true
		}
	}
	return false
}

const incidentStoreVersion = 1

type incidentStore struct {
	Version   uint64     `json:"version"`
	Incidents []Incident `json:"incidents"`
}

func (runtime *Runtime) incidentsPath() string {
	return filepath.Join(runtime.config.DataDir, "incidents.json")
}

// persistIncidentsLocked rewrites the incident store atomically. Callers must
// hold incidentMu.
func (runtime *Runtime) persistIncidentsLocked() error {
	store := incidentStore{Version: incidentStoreVersion, Incidents: runtime.incidents}
	data, err := json.Marshal(store)
	if err != nil {
		return err
	}
	path := runtime.incidentsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".next"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (runtime *Runtime) loadIncidents() error {
	incidentMu.Lock()
	defer incidentMu.Unlock()
	data, err := os.ReadFile(runtime.incidentsPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var store incidentStore
	if err := json.Unmarshal(data, &store); err != nil {
		return fmt.Errorf("load incident store: %w", err)
	}
	if store.Version != incidentStoreVersion {
		return fmt.Errorf("incident store version %d is unsupported", store.Version)
	}
	for _, incident := range store.Incidents {
		if incident.ID == "" || len(incident.Occurrences) == 0 || !validSeverity(incident.Severity) {
			return fmt.Errorf("incident store holds an invalid incident")
		}
	}
	runtime.incidents = store.Incidents
	return nil
}
