package agenthub

import "errors"

var (
	ErrOffline      = errors.New("agent offline")
	ErrSlowConsumer = errors.New("agent send buffer full")
	ErrTimeout      = errors.New("agent request timed out")
)
