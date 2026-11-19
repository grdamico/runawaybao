// Package runtimeevents observes and attributes execution events. It has no
// OpenBao client, credential, revocation, or enforcement dependency.
package runtimeevents

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrEvent = errors.New("invalid runtime event")

type Namespace struct {
	Inum uint32 `json:"inum"`
	Host bool   `json:"is_host"`
}

type Process struct {
	Namespaces *struct {
		PID    Namespace `json:"pid"`
		Cgroup Namespace `json:"cgroup"`
	} `json:"ns"`
	ExecID       string    `json:"exec_id"`
	ParentExecID string    `json:"parent_exec_id"`
	PID          uint32    `json:"pid"`
	Docker       string    `json:"docker"`
	Start        time.Time `json:"start_time"`
	Binary       string    `json:"binary"`
	Pod          *struct {
		Container struct {
			ID string `json:"id"`
		} `json:"container"`
	} `json:"pod"`
}

// Event deliberately omits arguments, environment, and raw payloads.
type Event struct {
	ParentExecID string
	ParentPID    uint32
	Process      Process
	FullID       string
	Node         string
	Time         time.Time
}

func Parse(b []byte) (Event, string, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(b, &envelope) != nil || envelope == nil {
		return Event{}, "", ErrEvent
	}
	var exec struct {
		Process Process `json:"process"`
		Parent  Process `json:"parent"`
	}
	var result Event
	if _, ok := envelope["process_throttle"]; ok {
		return result, "loss", nil
	}
	if _, ok := envelope["rate_limit_info"]; ok {
		return result, "loss", nil
	}
	if _, ok := envelope["process_exec"]; !ok {
		for _, kind := range []string{"process_exit", "process_kprobe", "process_tracepoint", "process_loader", "process_uprobe", "process_lsm", "process_usdt"} {
			if _, ok := envelope[kind]; ok {
				return result, "other", nil
			}
		}
		return result, "unknown", nil
	}
	count := 0
	for k := range envelope {
		if strings.HasPrefix(k, "process_") {
			count++
		}
	}
	if count != 1 {
		return result, "", ErrEvent
	}
	if json.Unmarshal(envelope["process_exec"], &exec) != nil {
		return result, "", ErrEvent
	}
	if v, ok := envelope["node_name"]; ok && json.Unmarshal(v, &result.Node) != nil {
		return result, "", ErrEvent
	}
	if v, ok := envelope["time"]; ok && json.Unmarshal(v, &result.Time) != nil {
		return result, "", ErrEvent
	}
	result.Process = exec.Process
	result.ParentExecID = exec.Parent.ExecID
	result.ParentPID = exec.Parent.PID
	for _, p := range []Process{exec.Process, exec.Parent} {
		if len(p.ExecID) > 1024 || len(p.ParentExecID) > 1024 || len(p.Binary) > 4096 || len(p.Docker) > 128 {
			return Event{}, "", ErrEvent
		}
	}
	if len(result.Node) > 1024 {
		return Event{}, "", ErrEvent
	}
	if exec.Process.Pod != nil {
		result.FullID = exec.Process.Pod.Container.ID
		if len(result.FullID) > 128 {
			return Event{}, "", ErrEvent
		}
	}
	return result, "exec", nil
}
