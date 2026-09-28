package service

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Interlock struct {
	Name      string    `json:"name"`
	Satisfied bool      `json:"satisfied"`
	Detail    string    `json:"detail"`
	CheckedAt time.Time `json:"checked_at"`
}

func (runtime *Runtime) Interlocks() []Interlock {
	incidentMu.Lock()
	defer incidentMu.Unlock()
	return runtime.interlocksLocked(time.Now().UTC())
}

// interlocksLocked builds the full interlock snapshot. Callers must hold
// incidentMu; equipment interlocks do not touch incident state, while the
// incident reset interlock reads the incident slice directly.
func (runtime *Runtime) interlocksLocked(now time.Time) []Interlock {
	_, _, _, threaded, _ := runtime.web.Snapshot()
	status, _ := runtime.dryerControl.Status()
	ready, _, _, fault, _ := runtime.folder.Snapshot()
	values := []Interlock{
		{Name: "web_threaded", Satisfied: threaded, Detail: "paper web is fenced and threaded", CheckedAt: now},
		{Name: "dryer_ready", Satisfied: status == "ready", Detail: "dryer damper and profile startup are complete", CheckedAt: now},
		{Name: "folder_ready", Satisfied: ready, Detail: fault, CheckedAt: now},
		{Name: "press_stopped_for_splice", Satisfied: runtime.pressControl.CanSplice(), Detail: "current stop epoch has zero-speed proof", CheckedAt: now},
		runtime.incidentResetInterlockLocked(now),
	}
	sort.Slice(values, func(left, right int) bool { return values[left].Name < values[right].Name })
	return values
}

// incidentResetInterlockLocked is unsatisfied while any critical fault is not
// resolved: the associated operations stay blocked until the reset review
// approves. Callers must hold incidentMu.
func (runtime *Runtime) incidentResetInterlockLocked(now time.Time) Interlock {
	blocking := runtime.blockingIncidentsLocked()
	if len(blocking) == 0 {
		return Interlock{Name: "incident_reset", Satisfied: true, Detail: "no unresolved critical incidents", CheckedAt: now}
	}
	references := make([]string, 0, len(blocking))
	for _, incident := range blocking {
		references = append(references, fmt.Sprintf("%s(%s)", incident.Component, incident.ID))
	}
	return Interlock{
		Name:      "incident_reset",
		Satisfied: false,
		Detail:    fmt.Sprintf("%d unresolved critical incident(s): %s", len(blocking), strings.Join(references, ", ")),
		CheckedAt: now,
	}
}
